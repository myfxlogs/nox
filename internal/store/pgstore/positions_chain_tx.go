package pgstore

import (
	"context"
	"fmt"

	"arb/internal/data/keydomain"

	"github.com/jackc/pgx/v5"
)

// 审计 P1-8：CreatePosition 链事务包裹——CreatePosition + 全部 Legs 在单事务内原子执行。
// 从 positions.go 提取（文件超 300 行软警告），事务化内核函数供 CreatePositionWithLegs 复用。

// CreatePositionWithLegs CreatePosition + 全部 Legs 在单事务内原子执行。
// 任一 leg 写入失败 → 整体 ROLLBACK（position 行不留孤儿），避免 autocommit 序列下
// "position 已建+部分 leg 已写+崩溃"的不一致态（原依赖 reconcile/ClaimClose 补偿链恢复）。
// legs 为空时等价于 CreatePosition（仅建 position 行）。
// 注意：主执行路径（pipeline_execute.go）因 broker 调用在 CreatePosition 与 Legs 之间，
// 无法用本方法（事务不可跨 broker 调用）；本方法供 legs 已知的前置创建/测试/恢复路径使用。
func (s *Store) CreatePositionWithLegs(ctx context.Context, p *Position, legs []*PositionLeg) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("create position with legs (begin): %w", err)
	}
	defer tx.Rollback(ctx) // commit 后 no-op

	id, err := createPositionInTx(ctx, tx, p)
	if err != nil {
		return 0, err
	}
	for _, leg := range legs {
		leg.PositionID = id
		if err := createPositionLegInTx(ctx, tx, leg); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("create position with legs (commit): %w", err)
	}
	s.notifyPositionChange() // D-469b S3：commit 成功后触发（防终端态风暴=失败路径不发）
	return id, nil
}

// createPositionInTx 在给定事务内建 position 行（CreatePosition 的事务化内核）。
func createPositionInTx(ctx context.Context, tx pgx.Tx, p *Position) (int64, error) {
	var reqID, dedupKey, env interface{}
	if p.RequestID != "" {
		reqID = p.RequestID
	}
	if p.DedupKey != "" {
		dedupKey = p.DedupKey
	}
	if p.Environment != "" {
		env = p.Environment
	}
	q := `INSERT INTO positions (opportunity_id, type, status, expected_net_bps, opened_at, notional_usd,
			request_id, dedup_key, environment)
		VALUES ($1, $2, $3, $4, now(), $5, $6, $7, $8) RETURNING id`
	var id int64
	err := tx.QueryRow(ctx, q, p.OpportunityID, p.Type, p.Status, p.ExpectedNetBps, p.NotionalUSD,
		reqID, dedupKey, env).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create position: %w", err)
	}
	return id, nil
}

// createPositionLegInTx 在给定事务内写单腿记录（CreatePositionLeg 的事务化内核）。
// 语义与 CreatePositionLeg 一致：ON CONFLICT DO NOTHING + 0 行→ErrTerminalState。
func createPositionLegInTx(ctx context.Context, tx pgx.Tx, leg *PositionLeg) error {
	vk := keydomain.Parse(leg.Venue) // D-496：复合键拆裸键+通道型落库
	q := `INSERT INTO position_legs (position_id, leg_no, phase, attempt_no, venue, venue_type, symbol, side, qty,
		price_expected, price_actual, order_id, status,
		parent_attempt_id, client_order_id, requested_qty, filled_qty, avg_fill_price,
		fee_raw, fee_usd, submitted_at, uncertain_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		ON CONFLICT (position_id, leg_no, phase, attempt_no) DO NOTHING`
	ct, err := tx.Exec(ctx, q,
		leg.PositionID, leg.LegNo, leg.Phase, leg.AttemptNo, vk.Venue, vk.Type, leg.Symbol, leg.Side,
		leg.Qty, leg.PriceExpected, leg.PriceActual, leg.OrderID, leg.Status,
		leg.ParentAttemptID, leg.ClientOrderID, leg.RequestedQty, leg.FilledQty, leg.AvgFillPrice,
		leg.FeeRaw, leg.FeeUSD, leg.SubmittedAt, leg.UncertainAt)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrTerminalState
	}
	return nil
}
