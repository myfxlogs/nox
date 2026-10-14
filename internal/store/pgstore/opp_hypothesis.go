package pgstore

import (
	"context"
	"fmt"
	"time"
)

// OppHypothesis 是 fin_opp_hypothesis 一行（新品类假设，S1 只到 proposed+memo）。
type OppHypothesis struct {
	ID                      int64
	Venue, CategoryKey      string
	InstType, Shape         string
	ParamsProto             []byte // jsonb 原样（LLM 预审参数原型草案；nil=无）
	FamilyFit, Memo         string
	Sample, Detail          string
	Status                  string
	DecidedBy, RejectReason string
	DecidedAt               *time.Time
	FirstSeen, LastSeen     time.Time
}

// UpsertHypothesis category_key 幂等锚：新键插入（xmax=0→inserted=true）；
// 键在册（含 rejected=弃件记忆抑重推）→只刷 last_seen/sample/detail，inserted=false。
func (s *Store) UpsertHypothesis(ctx context.Context, h OppHypothesis) (int64, bool, error) {
	var id int64
	var inserted bool
	err := s.pool.QueryRow(ctx, `
		INSERT INTO fin_opp_hypothesis (venue, category_key, inst_type, shape, sample, detail)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (category_key) DO UPDATE SET
			last_seen = now(), sample = EXCLUDED.sample, detail = EXCLUDED.detail
		RETURNING id, (xmax = 0)`,
		h.Venue, h.CategoryKey, h.InstType, h.Shape, h.Sample, h.Detail).
		Scan(&id, &inserted)
	if err != nil {
		return 0, false, fmt.Errorf("upsert hypothesis %s: %w", h.CategoryKey, err)
	}
	return id, inserted, nil
}

// UpdateHypothesisMemo LLM 预审结果回写（family_fit/params_proto/memo 三列，
// 不碰 status——批准键永不进 LLM 手）。
func (s *Store) UpdateHypothesisMemo(ctx context.Context, id int64, familyFit, memo string, proto []byte) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE fin_opp_hypothesis SET family_fit=$2, memo=$3, params_proto=$4 WHERE id=$1`,
		id, familyFit, memo, proto)
	if err != nil {
		return fmt.Errorf("update hypothesis memo #%d: %w", id, err)
	}
	return nil
}
