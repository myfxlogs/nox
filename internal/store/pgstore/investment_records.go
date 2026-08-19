// investment_records（记账功能，财务线工作项 2）存储。
// 结构化投资记录（决定/教训/观察），关联资产，独立轻量表（设计文档 §3）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// InvestmentRecord 对应 investment_records 表（设计文档 §3.2）。
// Kind 三态：decision（决定）/ lesson（教训）/ observation（观察）。
type InvestmentRecord struct {
	ID        int64
	Kind      string
	Asset     string
	Content   string
	Ts        time.Time
	UpdatedAt time.Time
}

// AddInvestmentRecord 插入一条投资记录，返回新 id。
func (s *Store) AddInvestmentRecord(ctx context.Context, kind, asset, content string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO investment_records (kind, asset, content)
		VALUES ($1, $2, $3)
		RETURNING id
	`, kind, asset, content).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("add investment record: %w", err)
	}
	return id, nil
}

// ListInvestmentRecords 按 kind/asset/内容子串筛选（空 = 不筛），时间倒序返回（limit≤0 默认 200）。
// kind 非法值由 service 层拒绝；此处按设计文档 SQL 原样动态 WHERE（D-043：筛选/搜索后端算）。
func (s *Store) ListInvestmentRecords(ctx context.Context, kind, asset, q string, limit, offset int) ([]InvestmentRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	// 子串匹配：非空筛选值包 %；空筛选传 '' 使 WHERE 对应分支恒真（设计文档 §4 公式原样）。
	assetLike := ""
	if asset != "" {
		assetLike = "%" + asset + "%"
	}
	qLike := ""
	if q != "" {
		qLike = "%" + q + "%"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, kind, asset, content, ts, updated_at
		FROM investment_records
		WHERE (kind = $1 OR $1 = '')
		  AND (asset ILIKE $2 OR $2 = '')
		  AND (content ILIKE $3 OR $3 = '')
		ORDER BY ts DESC, id DESC
		LIMIT $4 OFFSET $5
	`, kind, assetLike, qLike, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list investment records: %w", err)
	}
	defer rows.Close()
	var out []InvestmentRecord
	for rows.Next() {
		var r InvestmentRecord
		if err := rows.Scan(&r.ID, &r.Kind, &r.Asset, &r.Content, &r.Ts, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
