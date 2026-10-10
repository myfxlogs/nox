package dashboard

// D-167 折分：AutoMapBroker/autoMapVenue 自 broker_rpc_symbols.go 提取保行数（零行为变更）。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"
	"connectrpc.com/connect"
)

// AutoMapBroker 一键自动映射（D-066）。
// 用生产 alias 表 + 合约众数门禁自动识别+映射 broker symbol。
// 只自动应用 auto 状态（名字+合约匹配）；conflict/multi_hit/trade_disabled/no_match 不自动应用。
// 幂等：已有映射的 symbol 标 already_mapped 跳过。
func (s *Service) AutoMapBroker(ctx context.Context, req *connect.Request[v1.AutoMapBrokerRequest]) (*connect.Response[v1.AutoMapBrokerResponse], error) {
	venue := strings.TrimSpace(req.Msg.GetVenue())
	if venue == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("venue 不能为空"))
	}
	results, applied, skipped, err := s.autoMapVenue(ctx, venue)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.AutoMapBrokerResponse{Results: results, Applied: int32(applied), Skipped: int32(skipped)}), nil
}

// autoMapVenue 单 venue 自动映射内核（D-148 抽出：RPC 壳 + AutoMapSweep 周期面共用）。
// 幂等：已有映射跳过；只应用 auto（名字+合约匹配），conflict/multi_hit/trade_disabled/no_match 不自动应用。
func (s *Service) autoMapVenue(ctx context.Context, venue string) (results []*v1.AutoMapBrokerResult, applied, skipped int, err error) {
	groups, err := s.bm.BrokerSymbols(ctx, venue)
	if err != nil {
		return nil, 0, 0, connect.NewError(connect.CodeUnavailable, fmt.Errorf("拉取 %s 符号表失败: %w", venue, err))
	}
	all, err := s.store.ListSymbolMappings(ctx, "")
	if err != nil {
		return nil, 0, 0, connect.NewError(connect.CodeInternal, err)
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

	var newSyms []string   // 待补订阅的符号
	var newCanons []string // 新落 canonical（D-149 equiv 种子域）

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
		newCanons = append(newCanons, canon)
	}

	// 运行时生效：重推映射 + 补订阅 + swap 补轮
	if len(newSyms) > 0 {
		s.bm.ReloadSymbolMap(ctx, venue)
		s.bm.Resubscribe(venue, newSyms)
		if fs, err := s.bm.SwapFacts(ctx); err == nil {
			log.Printf("[auto-map] %s: %d facts 补轮", venue, len(fs))
		}
	}

	// D-149：equiv 种子以 venue 全量已映射 canonical 为域（存量+本轮新增）——
	// 幂等；挂 newSyms 闸外：class 域后续长进时无需新映射也能补种。
	var mapped []string
	for _, m := range all {
		if m.Venue == venue {
			mapped = append(mapped, m.Canonical)
		}
	}
	if n, err := s.seedAssetEquivVenue(ctx, venue, append(mapped, newCanons...)); err != nil {
		log.Printf("[auto-map] %s: equiv seed: %v", venue, err)
	} else if n > 0 {
		log.Printf("[auto-map] %s: equiv seed +%d", venue, n)
	}

	log.Printf("[auto-map] %s: applied=%d skipped=%d", venue, applied, skipped)
	return results, applied, skipped, nil
}
