// Broker 管理 RPC handler（docs/broker管理器实现方案.md §2/§3）。
// 错误统一走 connect error；凭据明文存储（任务约束，见方案 §1）。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"arb/internal/adapter"
	"arb/internal/store/pgstore"

	mt4pb "arb/internal/adapter/gen/mtapi/mt4"
	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AddBroker 验证并持久化账号，成功后动态启动适配器。
func (s *Service) AddBroker(ctx context.Context, req *connect.Request[v1.AddBrokerRequest]) (*connect.Response[v1.AddBrokerResponse], error) {
	venue := strings.TrimSpace(req.Msg.GetVenue())
	if venue == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("venue 不能为空"))
	}
	plat := req.Msg.GetPlatform()
	if plat != 0 && plat != 1 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("platform 必须为 0(MT4) 或 1(MT5)"))
	}
	acct := &pgstore.BrokerAccount{
		Venue:    venue,
		Platform: int(plat),
		Host:     req.Msg.GetHost(),
		Port:     int(req.Msg.GetPort()),
		User:     req.Msg.GetUser(),
		Password: req.Msg.GetPassword(),
		Server:   req.Msg.GetServer(),
	}
	// 账号类型真实佣金（bps/腿，D-036）；缺省 = 回退 config 默认
	if req.Msg.CommissionBps != nil {
		v := req.Msg.GetCommissionBps()
		acct.CommissionBps = &v
	}
	if err := s.bm.AddBroker(ctx, acct); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&v1.AddBrokerResponse{}), nil
}

// RemoveBroker 停止适配器并删除账号。
func (s *Service) RemoveBroker(ctx context.Context, req *connect.Request[v1.RemoveBrokerRequest]) (*connect.Response[v1.RemoveBrokerResponse], error) {
	if err := s.bm.RemoveBroker(ctx, req.Msg.GetVenue()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.RemoveBrokerResponse{}), nil
}

// ListBrokerAccounts 返回全部已配置账号。
func (s *Service) ListBrokerAccounts(ctx context.Context, req *connect.Request[v1.ListBrokerAccountsRequest]) (*connect.Response[v1.ListBrokerAccountsResponse], error) {
	accounts, err := s.store.ListBrokerAccounts(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*v1.BrokerAccount, 0, len(accounts))
	for _, a := range accounts {
		acct := &v1.BrokerAccount{
			Venue:     a.Venue,
			Platform:  int32(a.Platform),
			Host:      a.Host,
			Port:      int32(a.Port),
			User:      a.User,
			Server:    a.Server,
			Alias:     a.Alias,
			Status:    a.Status,
			CreatedAt: timestamppb.New(a.CreatedAt),
		}
		// 账号真实佣金（bps/腿，D-036）；nil = 未设置回退默认
		acct.CommissionBps = a.CommissionBps
		out = append(out, acct)
	}
	return connect.NewResponse(&v1.ListBrokerAccountsResponse{Accounts: out}), nil
}

// VerifyAccount 临时连接并读账户摘要，验证凭据有效性。
func (s *Service) VerifyAccount(ctx context.Context, req *connect.Request[v1.VerifyAccountRequest]) (*connect.Response[v1.VerifyAccountResponse], error) {
	host := strings.TrimSpace(req.Msg.GetHost())
	if host == "" || req.Msg.GetUser() == 0 || req.Msg.GetPassword() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("host / 账号 / 密码不能为空"))
	}
	switch req.Msg.GetPlatform() {
	case 0:
		bal, eq, cur, lev, err := verifyMT4(ctx, host, req.Msg.GetServer(), int32(req.Msg.GetPort()), req.Msg.GetUser(), req.Msg.GetPassword())
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		return connect.NewResponse(&v1.VerifyAccountResponse{Balance: bal, Equity: eq, Currency: cur, Leverage: lev}), nil
	case 1:
		bal, eq, cur, lev, err := verifyMT5(ctx, host, req.Msg.GetServer(), int32(req.Msg.GetPort()), req.Msg.GetUser(), req.Msg.GetPassword())
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		return connect.NewResponse(&v1.VerifyAccountResponse{Balance: bal, Equity: eq, Currency: cur, Leverage: lev}), nil
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("platform 必须为 0(MT4) 或 1(MT5)"))
	}
}

func verifyMT4(ctx context.Context, host, server string, port int32, user int64, pass string) (float64, float64, string, int32, error) {
	a := adapter.NewMT4Adapter("", host, server, port, user, pass)
	if err := a.Connect(ctx); err != nil {
		return 0, 0, "", 0, fmt.Errorf("MT4 连接失败: %w", err)
	}
	defer a.Stop()
	resp, err := a.MT4Client().AccountSummary(tokenCtx(ctx, a.Token()), &mt4pb.AccountSummaryRequest{Id: a.Token()})
	if err != nil {
		return 0, 0, "", 0, fmt.Errorf("MT4 账户验证失败: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return 0, 0, "", 0, fmt.Errorf("MT4 账户验证失败: %s", e.GetMessage())
	}
	r := resp.GetResult()
	return r.GetBalance(), r.GetEquity(), r.GetCurrency(), int32(r.GetLeverage()), nil
}

func verifyMT5(ctx context.Context, host, server string, port int32, user int64, pass string) (float64, float64, string, int32, error) {
	a := adapter.NewMT5Adapter("", host, server, port, user, pass)
	if err := a.Connect(ctx); err != nil {
		return 0, 0, "", 0, fmt.Errorf("MT5 连接失败: %w", err)
	}
	defer a.Stop()
	resp, err := a.MT5Client().AccountSummary(tokenCtx(ctx, a.Token()), &mt5pb.AccountSummaryRequest{Id: a.Token()})
	if err != nil {
		return 0, 0, "", 0, fmt.Errorf("MT5 账户验证失败: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return 0, 0, "", 0, fmt.Errorf("MT5 账户验证失败: %s", e.GetMessage())
	}
	r := resp.GetResult()
	return r.GetBalance(), r.GetEquity(), r.GetCurrency(), int32(r.GetLeverage()), nil
}

// BrokerSymbols 返回运行中适配器的服务器符号表（附 broker 分组，补映射搜索用）。
func (s *Service) BrokerSymbols(ctx context.Context, req *connect.Request[v1.BrokerSymbolsRequest]) (*connect.Response[v1.BrokerSymbolsResponse], error) {
	symbols, err := s.bm.BrokerSymbols(ctx, req.Msg.GetVenue())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	entries := make([]*v1.SymbolEntry, 0, len(symbols))
	for _, sg := range symbols {
		entries = append(entries, &v1.SymbolEntry{Symbol: sg.Symbol, Group: sg.Group, ContractSize: sg.ContractSize})
	}
	return connect.NewResponse(&v1.BrokerSymbolsResponse{Symbols: entries}), nil
}

// SetBrokerAccountAlias 设置账号别名；空串 = 清除。
func (s *Service) SetBrokerAccountAlias(ctx context.Context, req *connect.Request[v1.SetBrokerAccountAliasRequest]) (*connect.Response[v1.SetBrokerAccountAliasResponse], error) {
	if err := s.store.SetBrokerAccountAlias(ctx, req.Msg.GetVenue(), req.Msg.GetAlias()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.SetBrokerAccountAliasResponse{}), nil
}

// SetBrokerCommission 更新账号真实佣金（bps/腿，D-036）：持久化 + 运行时热生效。
// nil = 清除（回退 config 默认）；显式传值（含 0 = 免佣金账号）精确生效。
func (s *Service) SetBrokerCommission(ctx context.Context, req *connect.Request[v1.SetBrokerCommissionRequest]) (*connect.Response[v1.SetBrokerCommissionResponse], error) {
	venue := strings.TrimSpace(req.Msg.GetVenue())
	if venue == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("venue 不能为空"))
	}
	var bps *float64
	if req.Msg.CommissionBps != nil {
		v := req.Msg.GetCommissionBps()
		bps = &v
	}
	if err := s.store.SetBrokerCommission(ctx, venue, bps); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.bm.SetCommission(venue, bps)
	return connect.NewResponse(&v1.SetBrokerCommissionResponse{}), nil
}

// SetBrokerDisabled 停用/启用账号（D-033）：停用停适配器保留凭据，启用重新连接。
func (s *Service) SetBrokerDisabled(ctx context.Context, req *connect.Request[v1.SetBrokerDisabledRequest]) (*connect.Response[v1.SetBrokerDisabledResponse], error) {
	venue := req.Msg.GetVenue()
	var err error
	if req.Msg.GetDisabled() {
		err = s.bm.DisableBroker(ctx, venue)
	} else {
		err = s.bm.EnableBroker(ctx, venue)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.SetBrokerDisabledResponse{}), nil
}

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

// AutoMapBroker 一键自动映射（D-066）。
// 用生产 alias 表 + 合约众数门禁自动识别+映射 broker symbol。
// 只自动应用 auto 状态（名字+合约匹配）；conflict/multi_hit/trade_disabled/no_match 不自动应用。
// 幂等：已有映射的 symbol 标 already_mapped 跳过。
func (s *Service) AutoMapBroker(ctx context.Context, req *connect.Request[v1.AutoMapBrokerRequest]) (*connect.Response[v1.AutoMapBrokerResponse], error) {
	venue := strings.TrimSpace(req.Msg.GetVenue())
	if venue == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("venue 不能为空"))
	}
	groups, err := s.bm.BrokerSymbols(ctx, venue)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("拉取 %s 符号表失败: %w", venue, err))
	}
	all, err := s.store.ListSymbolMappings(ctx, "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// 已有映射集合（本 venue）
	venueMapped := make(map[string]bool)
	for _, m := range all {
		if m.Venue == venue {
			venueMapped[m.BrokerSymbol] = true
		}
	}
	// 合约众数画像
	contractProfile := make(map[string]float64) // canonical → 期望合约众数
	{
		counts := make(map[string]map[float64]int)
		for _, m := range all {
			if m.ContractSize != nil {
				if counts[m.Canonical] == nil {
					counts[m.Canonical] = make(map[float64]int)
				}
				counts[m.Canonical][*m.ContractSize]++
			}
		}
		for c, m := range counts {
			best, n := 0.0, 0
			for v, k := range m {
				if k > n {
					best, n = v, k
				}
			}
			contractProfile[c] = best
		}
	}

	var results []*v1.AutoMapBrokerResult
	applied, skipped := 0, 0
	var newSyms []string // 待补订阅的符号

	for _, g := range groups {
		brokerSym := g.Symbol
		result := &v1.AutoMapBrokerResult{
			BrokerSymbol: brokerSym,
			Group:        g.Group,
			ContractSize: g.ContractSize,
		}

		// 幂等：已有映射跳过
		if venueMapped[brokerSym] {
			result.Status = "already_mapped"
			results = append(results, result)
			skipped++
			continue
		}

		// TradeMode 预检（D-060 F7）
		if g.TradeMode == 0 {
			result.Status = "trade_disabled"
			result.Reason = "TradeMode=0 禁交易"
			results = append(results, result)
			skipped++
			continue
		}

		// alias 表匹配
		canon, hits := matchCanonicalDistinct(brokerSym)
		if hits == 0 {
			result.Status = "no_match"
			results = append(results, result)
			skipped++
			continue
		}
		if hits > 1 {
			result.Status = "multi_hit"
			result.Canonical = canon // 首命中
			result.Reason = fmt.Sprintf("匹配多个 canonical（%d），需人工定夺", hits)
			results = append(results, result)
			skipped++
			continue
		}

		// 唯一命中 → 合约校验
		result.Canonical = canon
		if exp, ok := contractProfile[canon]; ok && g.ContractSize > 0 && g.ContractSize != exp {
			result.Status = "conflict"
			result.Reason = fmt.Sprintf("合约 %v ≠ 桶期望 %v，需人工 override", g.ContractSize, exp)
			results = append(results, result)
			skipped++
			continue
		}

		// auto：写入映射
		var cp *float64
		if g.ContractSize > 0 {
			v := g.ContractSize
			cp = &v
		}
		if err := s.store.UpsertSymbolMapping(ctx, &pgstore.SymbolMapping{
			Venue: venue, BrokerSymbol: brokerSym, Canonical: canon, ContractSize: cp,
		}); err != nil {
			result.Status = "no_match"
			result.Reason = "写入失败: " + err.Error()
			results = append(results, result)
			skipped++
			continue
		}
		result.Status = "auto"
		results = append(results, result)
		applied++
		newSyms = append(newSyms, brokerSym)
	}

	// 运行时生效：重推映射 + 补订阅 + swap 补轮
	if len(newSyms) > 0 {
		s.bm.ReloadSymbolMap(ctx, venue)
		s.bm.Resubscribe(venue, newSyms)
		if fs, err := s.bm.SwapFacts(ctx); err == nil {
			log.Printf("[auto-map] %s: %d facts 补轮", venue, len(fs))
		}
	}

	log.Printf("[auto-map] %s: applied=%d skipped=%d", venue, applied, skipped)
	return connect.NewResponse(&v1.AutoMapBrokerResponse{Results: results, Applied: int32(applied), Skipped: int32(skipped)}), nil
}
