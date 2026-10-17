// xasset_kill_trigger.go —— kill 扳机数学化（判据落表净 PnL/连败双闸——
// 棘轮拆出件零行为变）。
package pgstore

import (
	"context"
	"fmt"
)

// DisableKillTriggeredCombos kill 扳机数学化（armed 评估环每拍调）：
// 判据落表——kill_net_pnl_usd（累计净 PnL 下限阈值）∨ kill_loss_streak
// （连续净亏结算 ≥N）——任一即摘 enabled=false+note 留痕（淘汰=可审计事件非删除；
// 归因簿行由后续告警面透——淘汰事件本身 log+note 留痕在库）。
// 归零语义：最近 N 笔结算连续 <0=连败（中间夹正结算=断计数）。
func (s *Store) DisableKillTriggeredCombos(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, canonical_buy, canonical_sell, venue_buy, venue_sell,
		       kill_net_pnl_usd, kill_loss_streak, note
		FROM xasset_allow_combos
		WHERE enabled=true AND (kill_net_pnl_usd IS NOT NULL OR kill_loss_streak IS NOT NULL)`)
	if err != nil {
		return 0, fmt.Errorf("list kill-gated combos: %w", err)
	}
	type cand struct {
		id             int64
		cb, cs, vb, vs string
		pnl            *float64
		streak         *int32
	}
	var cands []cand
	for rows.Next() {
		var c cand
		var note string
		if err := rows.Scan(&c.id, &c.cb, &c.cs, &c.vb, &c.vs, &c.pnl, &c.streak, &note); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan kill combo: %w", err)
		}
		cands = append(cands, c)
	}
	rows.Close()
	disabled := 0
	for _, c := range cands {
		// 双腿 EXISTS 定位该组合的 OPEN phase 持仓→结算净额链
		triggered := false
		var why string
		if c.pnl != nil {
			var net float64
			if err := s.pool.QueryRow(ctx, `
				SELECT COALESCE(SUM(COALESCE(s.net_realized_usd, s.realized_usd)),0)
				FROM position_settlements s
				JOIN positions p ON p.id = s.position_id
				WHERE EXISTS (SELECT 1 FROM position_legs lb WHERE lb.position_id=p.id
				              AND lb.phase='OPEN' AND lb.venue=$1 AND lb.symbol=$2)
				  AND EXISTS (SELECT 1 FROM position_legs ls WHERE ls.position_id=p.id
				              AND ls.phase='OPEN' AND ls.venue=$3 AND ls.symbol=$4)`,
				c.vb, c.cb, c.vs, c.cs).Scan(&net); err != nil {
				return disabled, fmt.Errorf("combo %d net pnl: %w", c.id, err)
			}
			if net < *c.pnl {
				triggered = true
				why = fmt.Sprintf("累计净 PnL %.2f < 阈 %.2f", net, *c.pnl)
			}
		}
		if !triggered && c.streak != nil {
			// 最近 N 笔结算连续净亏=连败断计数
			var streak int
			if err := s.pool.QueryRow(ctx, `
				WITH seq AS (
				  SELECT COALESCE(s.net_realized_usd, s.realized_usd) AS net,
				         ROW_NUMBER() OVER (ORDER BY s.settled_at DESC) rn
				  FROM position_settlements s
				  JOIN positions p ON p.id = s.position_id
				  WHERE EXISTS (SELECT 1 FROM position_legs lb WHERE lb.position_id=p.id
				                AND lb.phase='OPEN' AND lb.venue=$1 AND lb.symbol=$2)
				    AND EXISTS (SELECT 1 FROM position_legs ls WHERE ls.position_id=p.id
				                AND ls.phase='OPEN' AND ls.venue=$3 AND ls.symbol=$4)
				)
				-- 连败=最近起连续净亏（leading negatives——首笔非亏即断计数，
				-- 非「集内任一亏的 rn 最大值」旧误计）
				SELECT COUNT(*) FROM seq
				WHERE net < 0 AND rn <= COALESCE(
					(SELECT MIN(rn) FROM seq WHERE net >= 0), 2147483647)`,
				c.vb, c.cb, c.vs, c.cs).Scan(&streak); err != nil {
				return disabled, fmt.Errorf("combo %d streak: %w", c.id, err)
			}
			if int32(streak) >= *c.streak {
				triggered = true
				why = fmt.Sprintf("连败 %d ≥ 阈 %d", streak, *c.streak)
			}
		}
		if !triggered {
			continue
		}
		if _, err := s.pool.Exec(ctx, `
			UPDATE xasset_allow_combos SET enabled=false,
			note = note || '｜' || $1 WHERE id=$2`,
			fmt.Sprintf("kill 扳机摘：%s（淘汰数学化——可审计留痕非删除）", why), c.id); err != nil {
			return disabled, fmt.Errorf("disable kill combo %d: %w", c.id, err)
		}
		disabled++
	}
	return disabled, nil
}
