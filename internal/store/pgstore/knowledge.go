// knowledge_entries（经验库，§2.9）存储。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// KnowledgeEntry knowledge_entries 行。
// Verdict 为结论（如 'over_estimate'/'under_estimate'/'spot_on'）；Source 取值
// 'review'（统计复盘）/ 'manual'（人工录入）/ 'llm_review'（预留）。
type KnowledgeEntry struct {
	ID             int64
	Ts             time.Time
	Signature      string
	Venue          string
	Symbol         string
	Verdict        string
	Rationale      string
	Source         string
	Status         string // active / superseded / retracted
	ValidatedAt    *time.Time
	ValidationNote string
}

// ListKnowledgeEntries 返回经验库（默认 status=” 全部，limit≤0 默认 100）。
func (s *Store) ListKnowledgeEntries(ctx context.Context, status string, limit, offset int) ([]KnowledgeEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, ts, signature, venue, symbol, verdict, rationale, source, status, validated_at, validation_note
		FROM knowledge_entries
		WHERE ($1 = '' OR status = $1)
		ORDER BY ts DESC, id DESC
		LIMIT $2 OFFSET $3
	`, status, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list knowledge entries: %w", err)
	}
	defer rows.Close()
	var out []KnowledgeEntry
	for rows.Next() {
		var e KnowledgeEntry
		if err := rows.Scan(&e.ID, &e.Ts, &e.Signature, &e.Venue, &e.Symbol, &e.Verdict,
			&e.Rationale, &e.Source, &e.Status, &e.ValidatedAt, &e.ValidationNote); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpsertKnowledgeEntry 按 signature 唯一键幂等写入（§13.6 回流，重复运行不产生重复条目）。
func (s *Store) UpsertKnowledgeEntry(ctx context.Context, e *KnowledgeEntry) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO knowledge_entries (signature, venue, symbol, verdict, rationale, source)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (signature) DO UPDATE SET
			verdict = EXCLUDED.verdict,
			rationale = EXCLUDED.rationale,
			source = EXCLUDED.source,
			validated_at = NULL,
			validation_note = ''
		RETURNING id
	`, e.Signature, e.Venue, e.Symbol, e.Verdict, e.Rationale, e.Source).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert knowledge entry: %w", err)
	}
	return id, nil
}
