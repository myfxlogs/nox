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
// memoPending=family_fit IS NULL AND status='proposed'——上轮 memo 写败的死行由
// 下轮重试预审（不入册键只插不重试=空 memo 行会永久死库）。
func (s *Store) UpsertHypothesis(ctx context.Context, h OppHypothesis) (id int64, inserted, memoPending bool, err error) {
	err = s.pool.QueryRow(ctx, `
		INSERT INTO fin_opp_hypothesis (venue, category_key, inst_type, shape, sample, detail)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (category_key) DO UPDATE SET
			last_seen = now(), sample = EXCLUDED.sample, detail = EXCLUDED.detail
		RETURNING id, (xmax = 0), (family_fit IS NULL AND status='proposed')`,
		h.Venue, h.CategoryKey, h.InstType, h.Shape, h.Sample, h.Detail).
		Scan(&id, &inserted, &memoPending)
	if err != nil {
		return 0, false, false, fmt.Errorf("upsert hypothesis %s: %w", h.CategoryKey, err)
	}
	return id, inserted, memoPending, nil
}

// UpdateHypothesisMemo LLM 预审结果回写（family_fit/params_proto/memo 三列，
// 不碰 status——批准键永不进 LLM 手）。proto 空串→NULL（jsonb 不吃零长字节序=22P02）。
func (s *Store) UpdateHypothesisMemo(ctx context.Context, id int64, familyFit, memo, proto string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE fin_opp_hypothesis SET family_fit=$2, memo=$3,
			params_proto=NULLIF($4::text,'')::jsonb WHERE id=$1`,
		id, familyFit, memo, proto)
	if err != nil {
		return fmt.Errorf("update hypothesis memo #%d: %w", id, err)
	}
	return nil
}
