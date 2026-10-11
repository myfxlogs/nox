// dci_order.go —— D-191：fin_dci_order 幂等账本+执行回执面。
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// DCIOrder 申购单（request_id 幂等键防重发）。
type DCIOrder struct {
	ID         int64
	RequestID  string
	Venue      string
	TemplateID string
	ProductRef string
	Underlying string
	OptType    string
	Strike     *float64
	InvestCcy  string
	Qty        float64
	TenorDays  *int
	Status     string
	Response   string
	ProposalID *int64 // D-189：提案→订单回链（NULL=非提案触发）
}

// CreateDCIOrder 登记 pending 单（request_id 冲突=已受理→created=false 防重）。
func (s *Store) CreateDCIOrder(ctx context.Context, o *DCIOrder) (int64, bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO fin_dci_order
		  (request_id, venue, template_id, product_ref, underlying, opt_type,
		   strike, invest_ccy, qty, tenor_days, proposal_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (request_id) DO NOTHING
		RETURNING id`,
		o.RequestID, o.Venue, o.TemplateID, o.ProductRef, o.Underlying, o.OptType,
		o.Strike, o.InvestCcy, o.Qty, o.TenorDays, o.ProposalID).Scan(&id)
	if err != nil {
		return 0, false, nil // 冲突=幂等命中，非错
	}
	return id, true, nil
}

// FinishDCIOrder 收口（filled|failed|rejected+response 存证截断 2k）。
func (s *Store) FinishDCIOrder(ctx context.Context, id int64, status, response string) error {
	if len(response) > 2000 {
		response = response[:2000]
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE fin_dci_order SET status=$2, response=$3, resolved_at=now()
		WHERE id=$1 AND status='pending'`, id, status, response)
	if err != nil {
		return fmt.Errorf("finish dci order: %w", err)
	}
	return nil
}

// ListDCIQuotes 报价面选档（D-191-R8 序错修：strike 界+6h 时效窗下推 SQL，
// apy DESC LIMIT 作用于达标集——原 LIMIT50 先截断残集滤恒空+旧帧竞争）。
// D-191-R13 候选集去重：内层 DISTINCT ON (product_ref) 每品取最新帧（ts DESC），
// 外层再 apy DESC——同品旧版行零竞争、NULL 字段旧帧不挤档；ts 窗不收。
// strikeBound>0 时按 optType 方向过滤（PUT→strike≤bound、CALL→strike≥bound，
// strike 缺=不达标）；=0 不带界。
func (s *Store) ListDCIQuotes(ctx context.Context, venue, underlying, optType, investCcy string, maxTenorDays int, strikeBound float64) ([]CexDCIQuote, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT * FROM (
			SELECT DISTINCT ON (product_ref)
			       id, venue, underlying, invest_ccy, strike, opt_type, delivery_ts,
			       tenor_days, apy, product_ref, ts, order_ref,
			       min_amount, max_amount, purchase_decimal
			FROM fin_cex_dci_quote
			WHERE venue=$1 AND underlying=$2 AND opt_type=$3
			  AND ($4='' OR invest_ccy=$4)
			  AND (tenor_days IS NULL OR tenor_days <= $5)
			  AND ($6 <= 0 OR (strike IS NOT NULL AND
			       (($3='PUT' AND strike <= $6) OR ($3<>'PUT' AND strike >= $6))))
			  AND ts > now() - interval '6 hours'
			ORDER BY product_ref, ts DESC
		) latest
		ORDER BY apy DESC, ts DESC LIMIT 50`,
		venue, underlying, optType, investCcy, maxTenorDays, strikeBound)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CexDCIQuote
	for rows.Next() {
		var q CexDCIQuote
		if err := rows.Scan(&q.ID, &q.Venue, &q.Underlying, &q.InvestCcy, &q.Strike,
			&q.OptType, &q.DeliveryTs, &q.TenorDays, &q.APY, &q.ProductRef, &q.Ts, &q.OrderRef,
			&q.MinAmount, &q.MaxAmount, &q.PurchaseDecimal); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// InsertPersonalPosition 回执入账（kind='other' 合规 CHECK；qty/note/price_symbol）。
func (s *Store) InsertPersonalPosition(ctx context.Context, asset, kind, priceSymbol, note string, qty *float64, openedAt *time.Time) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO personal_positions (asset, kind, price_symbol, qty, note, opened_at)
		VALUES ($1,$2,$3,$4,$5,COALESCE($6,now())) RETURNING id`,
		asset, kind, priceSymbol, qty, note, openedAt).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert personal position: %w", err)
	}
	return id, nil
}

// OutstandingDCIQty D-191 R4：未到期 DCI 申购在外 qty（filled 且 created+tenor>now，
// 锁定币离现货账——托管池对账实有侧回填，防假漂移）。
func (s *Store) OutstandingDCIQty(ctx context.Context, venue, investCcy string) (float64, error) {
	var q float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(qty),0) FROM fin_dci_order
		WHERE venue=$1 AND invest_ccy=$2 AND status='filled'
		  AND created_at + COALESCE(tenor_days,0) * interval '1 day' > now()`,
		venue, investCcy).Scan(&q)
	return q, err
}

// RecentDCIOrderByProposal D-191-R11② 执行租约：同 proposal_id window 内最新
// 订单 id（无=0）——双击/重发防双成交（窗按库钟算）。
func (s *Store) RecentDCIOrderByProposal(ctx context.Context, proposalID int64, window time.Duration) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM fin_dci_order
		WHERE proposal_id=$1 AND created_at > now() - $2::interval
		ORDER BY id DESC LIMIT 1`, proposalID, window.String()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}
