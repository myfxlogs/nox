// CN-FI 投资笔记 actions 访问（从 cnfi.go 拆出，保 cnfi.go ≤300 行）。
package pgstore

import (
	"context"
	"fmt"
	"strings"
)

// AddCNFIAction records one operational action (投资笔记 D-242; amounts runtime-only).
func (s *Store) AddCNFIAction(ctx context.Context, code, layer, actionType string, qty, amountCny float64, navAtAction *float64, signalSnapshot, rationale, actionDate string) (int64, error) {
	if (actionType == "buy" || actionType == "sell") && strings.TrimSpace(rationale) == "" {
		return 0, fmt.Errorf("cnfi action %s: rationale 必填（投资笔记铁律）", actionType)
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO cnfi_actions (code, layer, action_type, qty, amount_cny, nav_at_action, signal_snapshot, rationale, action_date)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		code, layer, actionType, qty, amountCny, navAtAction, signalSnapshot, rationale, actionDate).Scan(&id)
	return id, err
}

// ListCNFIActions returns the newest actions (owner-only personal data; UI 展示合法，禁入 git).
func (s *Store) ListCNFIActions(ctx context.Context, limit int) ([]CNFIActionRow, error) {
	rs, err := s.pool.Query(ctx, `
		SELECT id, code, layer, action_type, qty::float8, amount_cny::float8,
		       COALESCE(signal_snapshot::text,''), rationale, action_date::text
		FROM cnfi_actions ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []CNFIActionRow
	for rs.Next() {
		var r CNFIActionRow
		if err := rs.Scan(&r.ID, &r.Code, &r.Layer, &r.ActionType, &r.Qty, &r.AmountCny,
			&r.SignalSnapshot, &r.Rationale, &r.ActionDate); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// CNFIActionRow is one operational action (投资笔记).
type CNFIActionRow struct {
	ID             int64
	Code           string
	Layer          string
	ActionType     string
	Qty            float64
	AmountCny      float64
	SignalSnapshot string
	Rationale      string
	ActionDate     string
}
