package pgstore

import (
	"context"
	"fmt"
)

// SumCashflowsNetForSettle D-438：结算专用净额。
// funding 实收以 income_events 为准（venue-native actual）；kind='funding' 现金流行=公式
// 投影轨——incomeRows>0 时整体替换不计入；incomeRows=0 时回落旧语义（按行 quality 计入）。
// hasFundingLeg=仓位有 FILLED OPEN 的 :perp 腿；fundingDue=持仓≥0.5d（跨≥1 funding 窗，
// 证据缺失才可判 unknown；短持无证据=合理零）。quality 语义同 SumCashflowsNetWithQuality。
func (s *Store) SumCashflowsNetForSettle(ctx context.Context, posID int64, hasFundingLeg, fundingDue bool) (sum float64, quality string, err error) {
	rows, err := s.pool.Query(ctx,
		`SELECT kind, COALESCE(amount_usd, 0), COALESCE(quality, 'actual')
		 FROM position_cashflows
		 WHERE position_id = $1 AND kind IN ('swap','funding','commission')`,
		posID)
	if err != nil {
		return 0, "", fmt.Errorf("sum cashflows net for settle: %w", err)
	}
	defer rows.Close()
	nonFundNet, cfFundNet := 0.0, 0.0
	var nonFundUnk, nonFundEst, fundUnk, fundEst, cfFundRows, nonFundRows bool
	for rows.Next() {
		var kind string
		var amt float64
		var q string
		if err := rows.Scan(&kind, &amt, &q); err != nil {
			return 0, "", err
		}
		unk, est := q == "unknown", q == "estimated"
		if kind == "funding" {
			cfFundRows = true
			fundUnk, fundEst = fundUnk || unk, fundEst || est
			if !unk {
				cfFundNet += amt
			}
		} else {
			nonFundRows = true
			nonFundUnk, nonFundEst = nonFundUnk || unk, nonFundEst || est
			if !unk {
				nonFundNet += amt
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, "", err
	}

	var incomeSum float64
	var incomeRows int
	if hasFundingLeg {
		err = s.pool.QueryRow(ctx,
			`SELECT COALESCE(SUM(amount_usd), 0), COUNT(*) FROM income_events
			 WHERE position_id = $1`,
			posID).Scan(&incomeSum, &incomeRows)
		if err != nil {
			return 0, "", fmt.Errorf("sum income events for settle: %w", err)
		}
	}

	net := nonFundNet
	fundQual := ""
	switch {
	case hasFundingLeg && incomeRows > 0:
		net += incomeSum
		fundQual = "actual"
	case hasFundingLeg && cfFundRows:
		net += cfFundNet
		switch {
		case fundUnk:
			fundQual = "unknown"
		case fundEst:
			fundQual = "estimated"
		default:
			fundQual = "actual"
		}
	case hasFundingLeg && fundingDue:
		fundQual = "unknown"
	}

	nonFundQual := ""
	if nonFundRows {
		switch {
		case nonFundUnk:
			nonFundQual = "unknown"
		case nonFundEst:
			nonFundQual = "estimated"
		default:
			nonFundQual = "actual"
		}
	}

	switch {
	case nonFundQual == "unknown" || fundQual == "unknown":
		return net, "unknown", nil
	case nonFundQual == "estimated" || fundQual == "estimated":
		return net, "estimated", nil
	case nonFundQual == "actual" || fundQual == "actual":
		return net, "actual", nil
	default:
		return 0, "no_cashflows", nil
	}
}
