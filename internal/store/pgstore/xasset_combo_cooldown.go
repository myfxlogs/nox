// xasset_combo_cooldown.go —— D-512 T9：combo 冷却闸（事故归因/净亏结算 → 30d 拒入）。
// 读侧派生不物化：LoadXAssetAllowCombos 注入冷却谓词，返回行附冷却截止供 UI 透出。
// 三态：clean（无命中）/ cooling（拒入+截止透出）/ expired（自动复资格）；
// enabled 业主闸语义不动——冷却是叠加自动层，只闸入场不伤在持仓。
package pgstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// 冷却参数（D-512 T9 SSOT 钉死）。
const (
	ComboCooldownWindow = 30 * 24 * time.Hour // 自最近命中结算起冷却 30d
	ComboCooldownLoss   = 0.0                 // 近 90d 结算净额 < 0 触发
)

// comboCooldownKey 组合键形（canonical×canonical×venueKey×venueKey）。
type comboCooldownKey struct{ cb, cs, vb, vs string }

// loadIncidentCooldowns 一次拉全部命中组合的最近事故结算时刻（派生不物化）。
// 归因路径：position_settlements(incident_attributed OR net<0 within 90d) → positions →
// position_legs(OPEN phase) 按 side 对腿（BUY→canonical_buy、SELL→canonical_sell，禁猜）。
func (s *Store) loadIncidentCooldowns(ctx context.Context, combos []XAssetAllowRow) (map[comboCooldownKey]time.Time, error) {
	out := map[comboCooldownKey]time.Time{}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (l.symbol, l2.symbol, l.venue, l2.venue)
		       l.symbol, l2.symbol, l.venue, l2.venue, s.settled_at
		FROM position_settlements s
		JOIN positions p          ON p.id = s.position_id
		JOIN position_legs l      ON l.position_id = p.id AND l.phase = 'OPEN' AND l.side = 'BUY'
		JOIN position_legs l2     ON l2.position_id = p.id AND l2.phase = 'OPEN' AND l2.side = 'SELL'
		WHERE s.settled_at >= now() - interval '90 days'
		  AND (s.incident_attributed
		       OR COALESCE(s.net_realized_usd, s.realized_usd) < $1)
		ORDER BY l.symbol, l2.symbol, l.venue, l2.venue, s.settled_at DESC`, ComboCooldownLoss)
	if err != nil {
		return nil, fmt.Errorf("combo incident cooldowns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cb, cs, vb, vs string
		var settled time.Time
		if err := rows.Scan(&cb, &cs, &vb, &vs, &settled); err != nil {
			return nil, err
		}
		out[comboCooldownKey{cb, cs, vb, vs}] = settled
	}
	return out, rows.Err()
}

// ApplyComboCooldown D-512 T9：LoadXAssetAllowCombos 的冷却叠加层。
// 返回 (过滤后行, 各行冷却截止（仅 cooling 行）)。expired=自动复资格（自然过期即消失）。
// 输入源三：①事故归因/净亏结算（loadIncidentCooldowns）+②复读指纹
// （loadRepeatCooldowns——settle 单消费者管线①，(combo,side,exit_reason)
// N 笔同纹且窗内累计亏<0→cooling）。
func (s *Store) ApplyComboCooldown(ctx context.Context, rows []XAssetAllowRow, now time.Time) ([]XAssetAllowRow, map[string]time.Time, error) {
	cool, err := s.loadIncidentCooldowns(ctx, rows)
	if err != nil {
		return nil, nil, fmt.Errorf("cooldown derive: %w", err)
	}
	// ② 复读指纹源并入——同键取更晚冷却起点（双源命中以更晚者为准）。
	if rep, rerr := s.loadRepeatCooldowns(ctx, rows); rerr == nil {
		for k, ts := range rep {
			if ts.After(cool[k]) {
				cool[k] = ts
			}
		}
	} // 复读源读败 fail-open=观察面降级不失主源（同 incident 源先例容灾）
	kept := make([]XAssetAllowRow, 0, len(rows))
	cutoffs := map[string]time.Time{}
	for _, r := range rows {
		// Load 行 venue=venue_key（带 :mt5/:perp 派生尾）；legs.venue=bare——
		// 键对齐用 bare 剥尾（D-423 monitorVenueKeys 逆操作，防口径分裂）。
		bare := func(v string) string {
			if i := strings.Index(v, ":"); i >= 0 {
				return v[:i]
			}
			return v
		}
		settled, hit := cool[comboCooldownKey{r.CanonicalBuy, r.CanonicalSell, bare(r.VenueBuy), bare(r.VenueSell)}]
		if !hit {
			kept = append(kept, r) // clean
			continue
		}
		until := settled.Add(ComboCooldownWindow)
		if now.After(until) {
			kept = append(kept, r) // expired=自动复资格
			continue
		}
		cutoffs[r.CanonicalBuy+"("+r.VenueBuy+")×"+r.CanonicalSell+"("+r.VenueSell+")"] = until
		// cooling：拒入（不进返回集）——只闸入场不伤在持仓。
	}
	return kept, cutoffs, nil
}

// 复读指纹参数（settle 单消费者管线①——判据落表纪律同 D-512 常量钉死）。
const (
	RepeatFingerprintMin    = 3                   // N 笔同纹
	RepeatFingerprintWindow = 90 * 24 * time.Hour // 滚动窗同 incident 源口径
)

// loadRepeatCooldowns ① 复读指纹第三输入源（settle 单消费者管线）：
// 指纹=(combo 键, side, exit_reason)——同一指纹 N 笔同纹且滚动窗累计净亏<0
// → 该 combo 冷却至最近退出+ComboCooldownWindow。
// 双源并采：
//
//	positions：combo_id（1057 归因列，含 venue 粒度=object_key 同域零 schema 变）
//	  → xasset_allow_combos 映射 canonical/venue 四元键；exit_reason=exit_kind
//	  既有列；净额=realized_pnl。
//	paper_spot_carry：组合键=(symbol,side,exit_reason)——venue_a/venue_b 与
//	  组合行 venue 集合对拍（同资产 carry 组合 canonical_buy==canonical_sell=
//	  symbol 形态命中）；净额=accrued+convergence。
//
// 滤非策略退出=exit_reason NOT IN ('venue_starved','data_lost','manual')
// （反证锚：venue 断供/数据丢失/人工退出不记复读——①只消费已归因 exit_reason）。
func (s *Store) loadRepeatCooldowns(ctx context.Context, combos []XAssetAllowRow) (map[comboCooldownKey]time.Time, error) {
	out := map[comboCooldownKey]time.Time{}
	bare := func(v string) string {
		if i := strings.Index(v, ":"); i >= 0 {
			return v[:i]
		}
		return v
	}
	// positions 侧：combo_id→allow 行四元键（IN 查表后按 id 映射）。
	type fpPos struct {
		comboID   int64
		side      string
		exitKind  string
		n         int
		sumNet    float64
		lastClose time.Time
	}
	prows, err := s.pool.Query(ctx, `
		SELECT combo_id, COALESCE(type,''), COALESCE(exit_kind,''), COUNT(*),
		       SUM(COALESCE(realized_pnl,0))::float8, MAX(closed_at)
		FROM positions
		WHERE combo_id IS NOT NULL AND status='CLOSED'
		  AND closed_at >= now() - interval '90 days'
		  AND exit_kind NOT IN ('manual','')
		GROUP BY combo_id, type, exit_kind
		HAVING COUNT(*) >= $1 AND SUM(COALESCE(realized_pnl,0)) < $2`,
		RepeatFingerprintMin, ComboCooldownLoss)
	if err != nil {
		return nil, fmt.Errorf("repeat fingerprint positions: %w", err)
	}
	var fps []fpPos
	var ids []int64
	for prows.Next() {
		var f fpPos
		if err := prows.Scan(&f.comboID, &f.side, &f.exitKind, &f.n, &f.sumNet, &f.lastClose); err == nil {
			fps = append(fps, f)
			ids = append(ids, f.comboID)
		}
	}
	prows.Close()
	if len(ids) > 0 {
		crows, err := s.pool.Query(ctx, `
			SELECT id, canonical_buy, canonical_sell, venue_key_buy, venue_key_sell
			FROM xasset_allow_combos WHERE id = ANY($1)`, ids)
		if err != nil {
			return nil, fmt.Errorf("repeat fingerprint combo map: %w", err)
		}
		keyByID := map[int64]comboCooldownKey{}
		for crows.Next() {
			var id int64
			var k comboCooldownKey
			if err := crows.Scan(&id, &k.cb, &k.cs, &k.vb, &k.vs); err == nil {
				k.vb, k.vs = bare(k.vb), bare(k.vs)
				keyByID[id] = k
			}
		}
		crows.Close()
		for _, f := range fps {
			if k, ok := keyByID[f.comboID]; ok && f.lastClose.After(out[k]) {
				out[k] = f.lastClose
			}
		}
	}
	// paper 侧：组指纹(symbol,side,exit_reason)→集合对拍命中组合行。
	type fpPaper struct {
		symbol, side, reason string
		n                    int
		sumNet               float64
		lastExit             time.Time
	}
	p2, err := s.pool.Query(ctx, `
		SELECT symbol, side, exit_reason, COUNT(*),
		       SUM(COALESCE(accrued_usd,0)+COALESCE(convergence_usd,0))::float8, MAX(exit_ts)
		FROM paper_spot_carry
		WHERE status='exited' AND exit_ts >= now() - interval '90 days'
		  AND exit_reason NOT IN ('venue_starved','data_lost','manual')
		GROUP BY symbol, side, exit_reason
		HAVING COUNT(*) >= $1
		   AND SUM(COALESCE(accrued_usd,0)+COALESCE(convergence_usd,0)) < $2`,
		RepeatFingerprintMin, ComboCooldownLoss)
	if err != nil {
		return nil, fmt.Errorf("repeat fingerprint paper: %w", err)
	}
	// paper venue 集合读（组内 venue_a/venue_b 并集——对拍用）。
	vrows, err := s.pool.Query(ctx, `
		SELECT symbol, side, exit_reason, array_agg(DISTINCT v) AS venues
		FROM paper_spot_carry, LATERAL (VALUES (venue_a),(venue_b)) AS u(v)
		WHERE status='exited' AND exit_ts >= now() - interval '90 days'
		GROUP BY symbol, side, exit_reason`)
	if err != nil {
		p2.Close()
		return nil, fmt.Errorf("repeat fingerprint paper venues: %w", err)
	}
	venueSet := map[string]map[string]bool{} // "symbol|side|reason"→venue 集合
	for vrows.Next() {
		var sym, sd, rs string
		var vs []string
		if err := vrows.Scan(&sym, &sd, &rs, &vs); err == nil {
			m := map[string]bool{}
			for _, v := range vs {
				m[bare(v)] = true
			}
			venueSet[sym+"|"+sd+"|"+rs] = m
		}
	}
	vrows.Close()
	for p2.Next() {
		var f fpPaper
		if err := p2.Scan(&f.symbol, &f.side, &f.reason, &f.n, &f.sumNet, &f.lastExit); err != nil {
			continue
		}
		vs := venueSet[f.symbol+"|"+f.side+"|"+f.reason]
		for _, r := range combos {
			cb, cs, vb, vsx := r.CanonicalBuy, r.CanonicalSell, bare(r.VenueBuy), bare(r.VenueSell)
			if cb != f.symbol && cs != f.symbol {
				continue // 组合不含该符号——非本指纹消费面
			}
			if !vs[vb] || !vs[vsx] {
				continue // venue 集合不对拍
			}
			k := comboCooldownKey{cb, cs, vb, vsx}
			if f.lastExit.After(out[k]) {
				out[k] = f.lastExit
			}
		}
	}
	p2.Close()
	return out, nil
}
