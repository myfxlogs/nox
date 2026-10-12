package dashboard

// service_cex_scan.go D-271：CEX 只读扫描器快照 RPC handler。
// GetCexScanSnapshot 读 DB（理财 APR + 跨所价差）+ funding facts + 账户缓存。

import (
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"

	v1 "nox/internal/gen/dashboardv1"
)

// spreadVerdict/fundingVerdict D-508 S8：原前端硬编码阈值（50bps / 年化10%）后端收编。
func spreadVerdict(bps float64) string {
	if bps >= 50 {
		return "opportunity"
	}
	return "not_opportunity"
}

func fundingVerdict(rate float64) string {
	if rate*100 >= 10 {
		return "opportunity"
	}
	return "not_opportunity"
}

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
			// D-508 S8：色阶阈值后端裁决（>=50bps=opportunity，前端只映射 VERDICT_COLOR）。
			Verdict: spreadVerdict(sp.SpreadBps),
		})
	}

	// Funding（读库）
	facts, err := s.store.LatestFactsByKind(ctx, "funding")
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
			// D-508 S8：年化>=10% 标 opportunity（阈值由前端迁入，单一判据源）。
			Verdict: fundingVerdict(f.Value),
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
				VenueKey:   info.VenueKey, // D-273：前端稳定 React key（P2-6）
				Holdings:   info.Holdings, // D-273：币种明细展开（P3-1）
			})
		}
	}

	// 判断结果（D-273）：reasons 入库为 JSON 数组字符串，此处 Unmarshal 还原为逐条 reason（N2 反向映射）
	judgments, err := s.store.LatestCexEarnJudgments(ctx)
	if err != nil {
		return nil, fmt.Errorf("latest cex earn judgments: %w", err)
	}
	var judgmentRows []*v1.CexEarnJudgmentRow
	for _, j := range judgments {
		row := &v1.CexEarnJudgmentRow{
			Venue:       j.Venue,
			Ccy:         j.Ccy,
			ProductType: j.ProductType,
			Apr:         j.APR,
			Category:    j.Category,
			Verdict:     j.Verdict,
		}
		if err := json.Unmarshal([]byte(j.Reasons), &row.Reasons); err != nil {
			row.Reasons = nil // 脏数据兜底：解析失败不阻断快照，前端显示无理由
		}
		judgmentRows = append(judgmentRows, row)
	}

	return connect.NewResponse(&v1.GetCexScanSnapshotResponse{
		EarnAprs:  earnRows,
		Spreads:   spreadRows,
		Fundings:  fundingRows,
		Balances:  balanceRows,
		Judgments: judgmentRows,
	}), nil
}
