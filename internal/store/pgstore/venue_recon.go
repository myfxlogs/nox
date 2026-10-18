// venue_recon.go —— D-539②：残差对账件读面（fin_venue_io/income_events 窗内流量求和）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// SumVenueIOExternal (venue,ccy) 窗内外部流量净值=Σdeposit−Σwithdraw
// （划转行 direction='transfer' 不计——那是 venue 内部移动，venue 总额不变）。
// venue 级求和：io 行归 canonical config（FilterIOAccounts 同凭据去重），
// 快照池行按 venue 聚合——按 config_id 会错配，按 venue 求和才覆盖全账户流水。
func (s *Store) SumVenueIOExternal(ctx context.Context, venue, ccy string, from, to time.Time) (float64, error) {
	var net float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE direction WHEN 'deposit' THEN amount WHEN 'withdraw' THEN -amount ELSE 0 END),0)
		FROM fin_venue_io
		WHERE venue=$1 AND ccy=$2 AND direction IN ('deposit','withdraw') AND ts>$3 AND ts<=$4`,
		venue, ccy, from, to).Scan(&net)
	if err != nil {
		return 0, fmt.Errorf("sum venue io external: %w", err)
	}
	return net, nil
}

// SumVenueIOTransfers (venue,ccy,wallet) 窗内划转净值=Σ(to_wallet=w)−Σ(from_wallet=w)
// （venue 级求和理由同 SumVenueIOExternal）。
// D-539② 残差双计修：earn-sub:% 行剔——申购同事件订单腿（earnQty）已覆盖，
// io transfer 再计=权益过解释假 critical（xmr-io 异键双写同族病灶）；
// earn-red:% 赎回无订单腿=单计不动。
func (s *Store) SumVenueIOTransfers(ctx context.Context, venue, ccy, wallet string, from, to time.Time) (float64, error) {
	var net float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN to_wallet=$3 THEN amount WHEN from_wallet=$3 THEN -amount ELSE 0 END),0)
		FROM fin_venue_io
		WHERE venue=$1 AND ccy=$2 AND direction='transfer' AND ts>$4 AND ts<=$5
		  AND (from_wallet=$3 OR to_wallet=$3)
		  AND COALESCE(external_id,'') NOT LIKE 'earn-sub:%%'`,
		venue, ccy, wallet, from, to).Scan(&net)
	if err != nil {
		return 0, fmt.Errorf("sum venue io transfers: %w", err)
	}
	return net, nil
}

// SumIncomeForPool (venue,venue_type,ccy) 窗内收益净额——symbol=ccy 行
// （earn_yield 等原生币种收益；合约级 FUNDING_FEE 属保证金域不入本口径）。
func (s *Store) SumIncomeForPool(ctx context.Context, venue, venueType, ccy string, from, to time.Time) (float64, error) {
	var sum float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_usd),0) FROM income_events
		WHERE venue=$1 AND venue_type=$2 AND symbol=$3 AND ts>$4 AND ts<=$5`,
		venue, venueType, ccy, from, to).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum income for pool: %w", err)
	}
	return sum, nil
}
