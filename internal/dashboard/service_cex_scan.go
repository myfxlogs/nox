package dashboard

// service_cex_scan.go D-271：CEX 只读扫描器快照 RPC handler。
// GetCexScanSnapshot 读 DB（理财 APR + 跨所价差）+ funding facts + 账户缓存。

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// GetCexScanSnapshot 返回 CEX 只读扫描器快照（理财 APR + 跨所价差 + funding + 余额）。
func (s *Service) GetCexScanSnapshot(ctx context.Context, req *connect.Request[v1.GetCexScanSnapshotRequest]) (*connect.Response[v1.GetCexScanSnapshotResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("store 未配置"))
	}

	// 理财 APR
	earnAPRs, err := s.store.LatestCexEarnAPRs(ctx)
	if err != nil {
		return nil, fmt.Errorf("latest cex earn aprs: %w", err)
	}
	var earnRows []*v1.CexEarnAPRRow
	for _, a := range earnAPRs {
		row := &v1.CexEarnAPRRow{
			Venue:       a.Venue,
			Ccy:         a.Ccy,
			ProductType: a.ProductType,
			Apr:         a.APR,
			Ts:          a.Ts.Format("2006-01-02T15:04:05Z07:00"),
		}
		if a.TierAPR.Valid {
			row.TierApr = &a.TierAPR.String
		}
		earnRows = append(earnRows, row)
	}

	// 跨所价差
	spreads, err := s.store.LatestCexSpreads(ctx)
	if err != nil {
		return nil, fmt.Errorf("latest cex spreads: %w", err)
	}
	var spreadRows []*v1.CexSpreadRow
	for _, sp := range spreads {
		spreadRows = append(spreadRows, &v1.CexSpreadRow{
			Symbol:    sp.Symbol,
			VenueBuy:  sp.VenueBuy,
			VenueSell: sp.VenueSell,
			PriceBuy:  sp.PriceBuy,
			PriceSell: sp.PriceSell,
			SpreadBps: sp.SpreadBps,
			Ts:        sp.Ts.Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	// Funding（读库）
	facts, err := s.store.LatestFacts(ctx, "funding", "", "")
	if err != nil {
		return nil, fmt.Errorf("latest funding facts: %w", err)
	}
	var fundingRows []*v1.CexFundingRow
	for _, f := range facts {
		fundingRows = append(fundingRows, &v1.CexFundingRow{
			Venue:  f.Venue,
			Symbol: f.Symbol,
			Rate:   f.Value,
			Ts:     f.Ts.Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	// 余额（读 AccountMonitor 缓存）
	var balanceRows []*v1.CexBalanceRow
	if s.acctMon != nil {
		for _, info := range s.acctMon.Snapshot() {
			if info == nil {
				continue
			}
			balanceRows = append(balanceRows, &v1.CexBalanceRow{
				Venue:      info.Venue,
				VenueType:  info.VenueType,
				Balance:    info.Balance,
				Equity:     info.Equity,
				FreeMargin: info.FreeMargin,
			})
		}
	}

	return connect.NewResponse(&v1.GetCexScanSnapshotResponse{
		EarnAprs: earnRows,
		Spreads:  spreadRows,
		Fundings: fundingRows,
		Balances: balanceRows,
	}), nil
}
