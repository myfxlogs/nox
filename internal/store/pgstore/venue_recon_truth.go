// venue_recon_truth.go —— D-630：真实净盈利权威口径=Σ venue 实盘权益差−Σ
// fin_venue_io 外部净流（入金正出金负——穿透归因中间层与账面伪影，venue
// 快照实拍对拍非推断）。稳定币池域同 residual_recon（spot/funding/earn×
// USDT/USD——MT 原币/CEX 他币池=口径外如实标缺）。首拍无基线=该池缺盖
// （coverage 缺面如实报，不补零冒真值）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// TrueNetProfit 权威净盈亏行（窗口双出：30d/全时由调用方给 since 早界）。
// 返修①：池枚举=全 venue_type×USDT/USD（perp/unified/mt5/futures 不再盲面——
// 返修前只取 spot/funding/earn=57% 本金盲面实证在案）。
type TrueNetProfit struct {
	NetUSD    float64
	Covered   []string // 覆盖池 venue|venue_type|ccy（有基线快照有现拍）
	Uncovered []string // 缺基线/缺现拍池（如实缺——漏计域标注）
}

// TrueNetProfitSince 窗口权威口径：Σ(现拍权益−基线权益)−Σ外部 io 净流。
// 基线=since 前最后一拍（无→该池 Uncovered 不计）；外部 io=deposit−withdraw
// venue 级（SumVenueIOExternal 同径）。
func (s *Store) TrueNetProfitSince(ctx context.Context, since, now time.Time) (*TrueNetProfit, error) {
	// 池清单=有快照的稳定币 spot/funding/earn 键域（与 residual 同源口径）。
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT venue, venue_type, currency FROM fin_venue_account_snapshot
		WHERE upper(currency) IN ('USDT','USD')`)
	if err != nil {
		return nil, fmt.Errorf("truth pools: %w", err)
	}
	type key struct{ v, vt, ccy string }
	var keys []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.v, &k.vt, &k.ccy); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	r := &TrueNetProfit{}
	var eqDelta float64
	for _, k := range keys {
		kn := k.v + "|" + k.vt + "|" + k.ccy
		cur, _, hasCur, err := s.PrevSnapshotEquity(ctx, k.v, k.vt, k.ccy, now.Add(time.Second))
		if err != nil {
			return nil, fmt.Errorf("truth cur %s: %w", kn, err)
		}
		base, _, hasBase, err := s.PrevSnapshotEquity(ctx, k.v, k.vt, k.ccy, since)
		if err != nil {
			return nil, fmt.Errorf("truth base %s: %w", kn, err)
		}
		if !hasCur || !hasBase {
			r.Uncovered = append(r.Uncovered, kn) // 缺基线/现拍=如实缺不冒零
			continue
		}
		r.Covered = append(r.Covered, kn)
		eqDelta += cur - base
	}
	// 外部 io 净流 venue 级（划转 transfer 不计=venue 内移动）。
	ioNet := 0.0
	vrows, err := s.pool.Query(ctx, `
		SELECT venue, COALESCE(SUM(CASE direction WHEN 'deposit' THEN amount WHEN 'withdraw' THEN -amount ELSE 0 END),0)
		FROM fin_venue_io WHERE ts>$1 AND ts<=$2 AND direction IN ('deposit','withdraw') GROUP BY venue`,
		since, now)
	if err != nil {
		return nil, fmt.Errorf("truth io: %w", err)
	}
	defer vrows.Close()
	for vrows.Next() {
		var v string
		var n float64
		if err := vrows.Scan(&v, &n); err != nil {
			return nil, err
		}
		ioNet += n
	}
	r.NetUSD = eqDelta - ioNet
	return r, vrows.Err()
}
