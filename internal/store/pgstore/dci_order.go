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
	CreatedAt  time.Time
	ResolvedAt *time.Time
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
// 候选集去重：内层 DISTINCT ON (product_ref) 每品取最新帧（ts DESC），
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

// InsertPersonalPosition 回执入账（kind='other' 合规 CHECK；qty/note/price_symbol；
// venue=机器归因列（1071 转正——空串=未归因如实写 NULL，禁塞别名义）。
func (s *Store) InsertPersonalPosition(ctx context.Context, asset, kind, priceSymbol, note string, qty *float64, openedAt *time.Time, venue string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO personal_positions (asset, kind, price_symbol, qty, note, opened_at, venue)
		VALUES ($1,$2,$3,$4,$5,COALESCE($6,now()),NULLIF($7,'')) RETURNING id`,
		asset, kind, priceSymbol, qty, note, openedAt, venue).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert personal position: %w", err)
	}
	return id, nil
}

// OutstandingDCIQty D-191 R4：未到期 DCI 申购在外 qty（filled 且 created+tenor>now，
// 锁定币离现货账——托管池对账实有侧回填，防假漂移）。
func (s *Store) OutstandingDCIQty(ctx context.Context, venue, investCcy string) (float64, error) {
	var q float64
	// D-207③：未平仓窗按 response.settleDate 回执原值截（替 created+tenor
	// 估算——估算有 +4h 过冲实证）；回执缺/不可解析行降回估算窗如实不猜。
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(qty),0) FROM fin_dci_order
		WHERE venue=$1 AND invest_ccy=$2 AND status='filled'
		  AND CASE
		      WHEN response ~ '^\s*\{' AND (response::jsonb->>'settleDate') ~ '^\d{10,15}$'
		      THEN to_timestamp((response::jsonb->>'settleDate')::double precision/1000.0)
		      ELSE created_at + COALESCE(tenor_days,0) * interval '1 day'
		      END > now()`,
		venue, investCcy).Scan(&q)
	return q, err
}

// RecentDCIOrderByProposal D-191-R11② 执行租约：同 proposal_id window 内最新
// **非 failed** 订单 id（无=0）——双击/重发防双成交；failed 终态不阻 D-198 续执
// （败单无仓位，阻它=禁重试，票面语义=在飞行/已成交行才挡）。
func (s *Store) RecentDCIOrderByProposal(ctx context.Context, proposalID int64, window time.Duration) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM fin_dci_order
		WHERE proposal_id=$1 AND status<>'failed'
		  AND created_at > now() - $2::interval
		ORDER BY id DESC LIMIT 1`, proposalID, window.String()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// FailedProposalOrder 断点续执候选行（提案+最新败单档向）。
type FailedProposalOrder struct {
	ProposalID int64
	ProductRef string
	OptType    string
}

// ListFailedProposalOrders D-198：proposed 提案中最新单为 failed 终态者——
// pending/submitted 飞行行与无单提案天然不入集；每提案最新一单按 max(id)。
func (s *Store) ListFailedProposalOrders(ctx context.Context) ([]FailedProposalOrder, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT o.proposal_id, o.product_ref, o.opt_type
		FROM fin_dci_order o
		JOIN fin_proposal p ON p.id=o.proposal_id AND p.status='proposed'
		WHERE o.status='failed'
		  AND o.id=(SELECT max(id) FROM fin_dci_order WHERE proposal_id=p.id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FailedProposalOrder
	for rows.Next() {
		var r FailedProposalOrder
		if err := rows.Scan(&r.ProposalID, &r.ProductRef, &r.OptType); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// OutstandingDCIProduct D-198 续执复验：同 product_ref+腿向 filled 未到期锁仓存在否——
// venue 实收但本地记 failed 的错位场景零重购判据。
func (s *Store) OutstandingDCIProduct(ctx context.Context, productRef, optType string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM fin_dci_order
			WHERE product_ref=$1 AND opt_type=$2 AND status='filled'
			  AND created_at + COALESCE(tenor_days,0) * interval '1 day' > now())`,
		productRef, optType).Scan(&ok)
	return ok, err
}

// DCIOrderLegCount D-198 返修②：同 proposal_id 该腿（opt_type+invest_ccy）
// fin_dci_order 行数——request_id 尝试序号 n=行数+1。
func (s *Store) DCIOrderLegCount(ctx context.Context, proposalID int64, optType, investCcy string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM fin_dci_order
		WHERE proposal_id=$1 AND opt_type=$2 AND invest_ccy=$3`,
		proposalID, optType, investCcy).Scan(&n)
	return n, err
}

// ListDCIOrders D-200②：订单近 N 单看板读面（id DESC；response 后端截≤300 字符）。
func (s *Store) ListDCIOrders(ctx context.Context, limit int) ([]DCIOrder, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, request_id, venue, template_id, product_ref, underlying, opt_type,
		       strike, invest_ccy, qty, tenor_days, status,
		       left(response, 300), proposal_id, created_at, resolved_at
		FROM fin_dci_order ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DCIOrder
	for rows.Next() {
		var o DCIOrder
		if err := rows.Scan(&o.ID, &o.RequestID, &o.Venue, &o.TemplateID, &o.ProductRef,
			&o.Underlying, &o.OptType, &o.Strike, &o.InvestCcy, &o.Qty, &o.TenorDays,
			&o.Status, &o.Response, &o.ProposalID, &o.CreatedAt, &o.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DCIOrderByID 单点读（D-542：ListDCIOrders limit>200 静默 clamp 20——清扫锚
// 旧单被抓不到实证）；缺行→nil,nil 如实空。
func (s *Store) DCIOrderByID(ctx context.Context, id int64) (*DCIOrder, error) {
	var o DCIOrder
	err := s.pool.QueryRow(ctx, `
		SELECT id, request_id, venue, template_id, product_ref, underlying, opt_type,
		       strike, invest_ccy, qty, tenor_days, status,
		       left(response, 300), proposal_id, created_at, resolved_at
		FROM fin_dci_order WHERE id=$1`, id).Scan(
		&o.ID, &o.RequestID, &o.Venue, &o.TemplateID, &o.ProductRef, &o.Underlying,
		&o.OptType, &o.Strike, &o.InvestCcy, &o.Qty, &o.TenorDays,
		&o.Status, &o.Response, &o.ProposalID, &o.CreatedAt, &o.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// DCIOrderIntentCount D-255③：意图稳定键尝试序——同「腿+产品+交割日」前缀的
// fin_dci_order 行数+1=新尝试号（proposal_id 无关——wheel 径无绑定也推进，
// 败单行不再 ON CONFLICT 假「已受理」）。
func (s *Store) DCIOrderIntentCount(ctx context.Context, reqPrefix string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM fin_dci_order WHERE request_id LIKE $1`,
		reqPrefix+"|%").Scan(&n)
	return n, err
}

// DCIOrderOpenIntent D-255③：同意图（前缀）在途/已成行存在性——
// pending|filled|settled 挡重发（幂等「已受理」），failed|rejected 不挡新尝试。
func (s *Store) DCIOrderOpenIntent(ctx context.Context, reqPrefix string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM fin_dci_order
		WHERE request_id LIKE $1 AND status IN ('pending','filled','settled')`,
		reqPrefix+"|%").Scan(&n)
	return n > 0, err
}

// LatestEarnActivityAt D-612 轮动持有窗读口：venue 最近一次 EARN 账本行
// （申购/轮动填单）时刻。无行→ok=false（无持仓史=窗约束不命中）。
func (s *Store) LatestEarnActivityAt(ctx context.Context, venue string) (time.Time, bool, error) {
	var ts *time.Time // max() 聚合恒回一行——无命中=NULL（非 ErrNoRows）
	err := s.pool.QueryRow(ctx, `
		SELECT max(created_at) FROM fin_dci_order
		WHERE venue=$1 AND opt_type='EARN' AND status<>'failed'`, venue).Scan(&ts)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("latest earn activity %s: %w", venue, err)
	}
	if ts == nil {
		return time.Time{}, false, nil
	}
	return *ts, true, nil
}
