// D-273 S5：earn APR 判断结果 CRUD（fin_cex_earn_judgment）。
// 本文件只依赖 pgstd 标准库+pgx，不导入 cexscan（依赖环防护，审计 P1-1）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// CexEarnJudgment fin_cex_earn_judgment 行（pgstore 存储类型）。
type CexEarnJudgment struct {
	ID          int64
	Venue       string
	Ccy         string
	ProductType string
	APR         float64
	Category    string
	Verdict     string
	Reasons     string // JSON array
	Ts          time.Time
}

// UpsertCexEarnJudgment 插入或更新（P2-5：upsert 去重，每 venue+ccy+product_type 一行）。
func (s *Store) UpsertCexEarnJudgment(ctx context.Context, r *CexEarnJudgment) error {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO fin_cex_earn_judgment (venue, ccy, product_type, apr, category, verdict, reasons)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (venue, ccy, product_type) DO UPDATE
		SET apr = EXCLUDED.apr, category = EXCLUDED.category,
		    verdict = EXCLUDED.verdict, reasons = EXCLUDED.reasons, ts = now()
		RETURNING id, ts
	`, r.Venue, r.Ccy, r.ProductType, r.APR, r.Category, r.Verdict, r.Reasons).Scan(&r.ID, &r.Ts)
	if err != nil {
		return fmt.Errorf("upsert cex earn judgment: %w", err)
	}
	return nil
}

// LatestCexEarnJudgments 取每 venue+ccy+product_type 的最新判断。
func (s *Store) LatestCexEarnJudgments(ctx context.Context) ([]CexEarnJudgment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (venue, ccy, product_type)
			id, venue, ccy, product_type, apr, category, verdict, reasons, ts
		FROM fin_cex_earn_judgment
		ORDER BY venue, ccy, product_type, ts DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("latest cex earn judgments: %w", err)
	}
	defer rows.Close()
	var out []CexEarnJudgment
	for rows.Next() {
		var r CexEarnJudgment
		if err := rows.Scan(&r.ID, &r.Venue, &r.Ccy, &r.ProductType, &r.APR, &r.Category, &r.Verdict, &r.Reasons, &r.Ts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
