// service_cex_finance.go —— D-365 S4：CEX 财务页 RPC（账户级盘点全币种+进出/收益流水）。
// 数据源：FullInventory（仅本 RPC 调用，monitor 管道零杂币请求——分层不变量）+ fin_venue_io
// + income_events（按 config 归属）。15min TTL 缓存防页面刷新风暴打交易所；前端零计算。
package dashboard

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

// cexFinanceTTL 财务页缓存窗（设计 S3：~15min 防刷新风暴）。
const cexFinanceTTL = 15 * time.Minute

// cexFinanceBoard TTL 缓存体（D-352 paperBoard 同款模式）。
var (
	cexFinanceMu       sync.Mutex
	cexFinanceCached   *v1.GetCexFinanceResponse
	cexFinanceCachedAt time.Time
)

// GetCexFinance 每 real CEX 配置一块：usdt_equity+全币种 holdings（带占比）+进出/收益流水。
func (s *Service) GetCexFinance(ctx context.Context, req *connect.Request[v1.GetCexFinanceRequest]) (*connect.Response[v1.GetCexFinanceResponse], error) {
	cexFinanceMu.Lock()
	if cexFinanceCached != nil && time.Since(cexFinanceCachedAt) < cexFinanceTTL {
		resp := cexFinanceCached
		cexFinanceMu.Unlock()
		return connect.NewResponse(resp), nil
	}
	cexFinanceMu.Unlock()

	cfgs, err := s.store.ListExchangeConfigs(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	winners := cexKeyWinners(cfgs)
	resp := &v1.GetCexFinanceResponse{}
	for i := range cfgs {
		c := &cfgs[i]
		if c.Simulated || (c.Venue != "binance" && c.Venue != "okx") || c.APIKey == "" || c.APISecret == "" {
			continue // real CEX only（D-363 FilterIOAccounts 同判据）
		}
		q := reconcile.NewCEXAccountQuerier(c.Venue, c.VenueType, c.APIKey, c.APISecret, c.Passphrase, resolveExchangeBaseURL(*c), c.Website, c.Simulated)
		inv, err := q.FullInventory(ctx)
		if err != nil {
			log.Printf("[cex-finance] %s:%s#%d full inventory: %v (块跳过)", c.Venue, c.VenueType, c.ID, err)
			continue
		}
		blk := &v1.CexAccountFinance{
			Venue: c.Venue, VenueType: c.VenueType,
			Key:          reconcile.CexVenueKey(c.Venue, c.VenueType, c.ID, winners),
			Simulated:    c.Simulated,
			UsdtEquity:   inv.UsdtEquity,
			InventoryUsd: inv.InventoryUSD,
		}
		// holdings：usdt_value 降序 + 占比（后端算）
		hs := inv.Holdings
		sort.Slice(hs, func(a, b int) bool { return hs[a].UsdtValue > hs[b].UsdtValue })
		for _, h := range hs {
			w := 0.0
			if inv.InventoryUSD > 0 {
				w = h.UsdtValue / inv.InventoryUSD * 100
			}
			blk.Holdings = append(blk.Holdings, &v1.CexFinanceHolding{
				Ccy: h.Ccy, Qty: h.Qty, UsdtValue: h.UsdtValue, WeightPct: w,
			})
		}
		blk.Flows = s.cexFinanceFlows(ctx, c.Venue, c.ID)
		resp.Accounts = append(resp.Accounts, blk)
	}
	// 账户块按 inventory 降序（大账户在前）
	sort.Slice(resp.Accounts, func(a, b int) bool {
		return resp.Accounts[a].InventoryUsd > resp.Accounts[b].InventoryUsd
	})

	cexFinanceMu.Lock()
	cexFinanceCached = resp
	cexFinanceCachedAt = time.Now()
	cexFinanceMu.Unlock()
	return connect.NewResponse(resp), nil
}

// dedupOkxTradingConfigs D-365-R1 S3：real CEX 配置 → 财务块清单（纯函数可测）。
// real-only 判据沿 FilterIOAccounts 同款；okx spot/perp 是同一统一账户 → 只保留一张
// unified 卡（min id 代表，VenueType 改 unified），binance spot/perp 是独立钱包不合并。
// 输出按 id 升序（确定性）。
func dedupOkxTradingConfigs(cfgs []pgstore.ExchangeConfig) []pgstore.ExchangeConfig {
	sorted := make([]pgstore.ExchangeConfig, len(cfgs))
	copy(sorted, cfgs)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].ID < sorted[b].ID }) // 首张=min id（确定性）
	var out []pgstore.ExchangeConfig
	okxTradingSeen := false
	for _, c := range sorted {
		if c.Simulated || (c.Venue != "binance" && c.Venue != "okx") || c.APIKey == "" || c.APISecret == "" {
			continue
		}
		if c.Venue == "okx" && (c.VenueType == "spot" || c.VenueType == "perp") {
			if okxTradingSeen {
				continue // 同一统一账户，spot/perp 双 config 只出一张卡（首张按 id 序）
			}
			okxTradingSeen = true
			// VenueType 保持原值（spot/perp）供 FullInventory 分派；渲染层标 unified
		}
		out = append(out, c)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// cexFinanceKey 财务块 key：okx unified 卡用 okx:unified，其余 CexVenueKey 同源语义。
func cexFinanceKey(c *pgstore.ExchangeConfig, winners map[string]int64) string {
	if c.Venue == "okx" && c.VenueType == "unified" {
		return "okx:unified"
	}
	return reconcile.CexVenueKey(c.Venue, c.VenueType, c.ID, winners)
}

// cexFinanceFlows 进出（fin_venue_io）+收益（income_events）合并，ts 倒序，各 50 上限。
func (s *Service) cexFinanceFlows(ctx context.Context, venue string, configID int64) []*v1.CexFinanceFlow {
	var flows []*v1.CexFinanceFlow
	if ios, err := s.store.RecentVenueIO(ctx, venue, configID, 50); err == nil {
		for _, r := range ios {
			flows = append(flows, &v1.CexFinanceFlow{
				Ts: r.Ts.Format(time.RFC3339), Kind: r.Direction, Ccy: r.Ccy,
				Amount: r.Amount, Fee: derefF(r.Fee), Status: r.Status,
			})
		}
	} else {
		log.Printf("[cex-finance] io %s#%d: %v", venue, configID, err)
	}
	if inc, err := s.store.RecentIncomeByConfig(ctx, venue, configID, 50); err == nil {
		for _, e := range inc {
			flows = append(flows, &v1.CexFinanceFlow{
				Ts: e.Ts.Format(time.RFC3339), Kind: e.IncomeType, Ccy: e.Symbol,
				Amount: e.AmountUSD,
			})
		}
	} else {
		log.Printf("[cex-finance] income %s#%d: %v", venue, configID, err)
	}
	sort.Slice(flows, func(a, b int) bool { return flows[a].Ts > flows[b].Ts })
	if len(flows) > 100 {
		flows = flows[:100]
	}
	return flows
}

func derefF(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
