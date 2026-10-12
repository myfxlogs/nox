package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"nox/internal/store/pgstore"

	"connectrpc.com/connect"
	v1 "nox/internal/gen/dashboardv1"
)

// --- Symbol 映射 ---

// ListSymbolMappings 列出映射；venue 空 = 全部。
func (s *Service) ListSymbolMappings(ctx context.Context, req *connect.Request[v1.ListSymbolMappingsRequest]) (*connect.Response[v1.ListSymbolMappingsResponse], error) {
	mappings, err := s.store.ListSymbolMappings(ctx, req.Msg.GetVenue())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	pb := make([]*v1.SymbolMapping, 0, len(mappings))
	for _, mp := range mappings {
		pb = append(pb, &v1.SymbolMapping{
			Venue:        mp.Venue,
			BrokerSymbol: mp.BrokerSymbol,
			Canonical:    mp.Canonical,
			ContractSize: mp.ContractSize,
		})
	}
	return connect.NewResponse(&v1.ListSymbolMappingsResponse{Mappings: pb}), nil
}

// SaveSymbolMapping 写入映射；保存后重推适配器符号映射 + 补订阅（运行时生效）。
// D-042 合约规格校验（零信任）：合约只信 broker 自取（SymbolParams 权威值，前端不可传值伪造）；
// 与 canonical 期望画像不符（同标的跨 broker 合约必一致，不符 = 假映射，套利对比不可靠）→ 默认拒绝
// 并给理由；override=true 为人工确认强制保存（留审计日志）。MT4 / 合约未知 → 跳过校验（不做无依据判断）。
func (s *Service) SaveSymbolMapping(ctx context.Context, req *connect.Request[v1.SaveSymbolMappingRequest]) (*connect.Response[v1.SaveSymbolMappingResponse], error) {
	venue := strings.TrimSpace(req.Msg.GetVenue())
	brokerSym := strings.TrimSpace(req.Msg.GetBrokerSymbol())
	canonical := strings.TrimSpace(req.Msg.GetCanonical())
	if venue == "" || brokerSym == "" || canonical == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("venue / broker_symbol / canonical 不能为空"))
	}
	// 权威合约：从 broker 自取（零信任）。known=false → 跳过规格校验，存 NULL 合约。
	contract, known, err := s.bm.BrokerContractSize(ctx, venue, brokerSym)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("读取 %s 合约规格失败: %w", brokerSym, err))
	}
	if known && !req.Msg.GetOverride() {
		exp, ok, err := s.store.CanonicalContractProfile(ctx, canonical)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if ok && contract != exp {
			reason := fmt.Sprintf("合约规格校验失败：%s 合约 %v 与 %s 期望 %v 不符，套利对比不可靠。确认无误请强制保存",
				brokerSym, contract, canonical, exp)
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(reason))
		}
	}
	var cp *float64
	if known {
		cp = &contract
	}
	if req.Msg.GetOverride() {
		log.Printf("[broker] 人工确认强制保存映射 %s: %s → %s（合约 %v，已知=%v）", venue, brokerSym, canonical, contract, known)
	}
	if err := s.store.UpsertSymbolMapping(ctx, &pgstore.SymbolMapping{
		Venue: venue, BrokerSymbol: brokerSym, Canonical: canonical, ContractSize: cp,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.bm.ReloadSymbolMap(ctx, venue)
	// D-047 缺陷③：新增映射的 raw 符号若不补订阅，无报价流 → "保存了不生效"。
	s.bm.Resubscribe(venue, []string{brokerSym})
	return connect.NewResponse(&v1.SaveSymbolMappingResponse{}), nil
}

// ListCanonicals 返回既有 canonical 全集（生产 alias 表 ∪ 全部已映射 canonical，去重排序），
// 供前端 canonical 下拉选项。选项由后端计算（零信任，前端仅渲染，不自派）。D-042/D-066。
func (s *Service) ListCanonicals(ctx context.Context, req *connect.Request[v1.ListCanonicalsRequest]) (*connect.Response[v1.ListCanonicalsResponse], error) {
	all, err := s.store.ListSymbolMappings(ctx, "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	set := make(map[string]struct{}, len(all)+len(canonicalAliases))
	for _, c := range allCanonicals() {
		set[c] = struct{}{}
	}
	for _, mp := range all {
		set[mp.Canonical] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return connect.NewResponse(&v1.ListCanonicalsResponse{Canonicals: out}), nil
}

// DeleteSymbolMapping 删除映射并重推适配器符号映射（mtapi 无反订阅，会话残留订阅由 adapter
// 对称映射回退为原符号，engine 侧无害——归档同况）。
func (s *Service) DeleteSymbolMapping(ctx context.Context, req *connect.Request[v1.DeleteSymbolMappingRequest]) (*connect.Response[v1.DeleteSymbolMappingResponse], error) {
	if err := s.store.DeleteSymbolMapping(ctx, req.Msg.GetVenue(), req.Msg.GetBrokerSymbol()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.bm.ReloadSymbolMap(ctx, req.Msg.GetVenue())
	return connect.NewResponse(&v1.DeleteSymbolMappingResponse{}), nil
}

// ListMappingSuggestions 返回该 venue 名字匹配既有 canonical 但未入库的建议映射（D-042）。
// status=auto：名字+合约匹配（一键采纳）；conflict：合约不符（待确认，采纳需 override）。
// 数据源 = classifySymbols（与 add-broker 自动映射同一判据，单一真相）。
func (s *Service) ListMappingSuggestions(ctx context.Context, req *connect.Request[v1.ListMappingSuggestionsRequest]) (*connect.Response[v1.ListMappingSuggestionsResponse], error) {
	venue := req.Msg.GetVenue()
	groups, err := s.bm.BrokerSymbols(ctx, venue)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	all, err := s.store.ListSymbolMappings(ctx, "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	venueMapped := make(map[string]bool)
	for _, m := range all {
		if m.Venue == venue {
			venueMapped[m.BrokerSymbol] = true
		}
	}
	idx := buildCanonicalIdx(all, defaultSymbols)
	var out []*v1.SymbolMappingSuggestion
	for _, sug := range classifySymbols(groups, idx) {
		if venueMapped[sug.BrokerSymbol] {
			continue
		}
		out = append(out, &v1.SymbolMappingSuggestion{
			BrokerSymbol: sug.BrokerSymbol,
			Group:        sug.Group,
			ContractSize: sug.ContractSize,
			Canonical:    sug.Canonical,
			Status:       string(sug.Status),
			Reason:       sug.Reason,
		})
	}
	return connect.NewResponse(&v1.ListMappingSuggestionsResponse{Suggestions: out}), nil
}
