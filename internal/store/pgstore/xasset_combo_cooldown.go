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
func (s *Store) ApplyComboCooldown(ctx context.Context, rows []XAssetAllowRow, now time.Time) ([]XAssetAllowRow, map[string]time.Time, error) {
	cool, err := s.loadIncidentCooldowns(ctx, rows)
	if err != nil {
		return nil, nil, fmt.Errorf("cooldown derive: %w", err)
	}
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
