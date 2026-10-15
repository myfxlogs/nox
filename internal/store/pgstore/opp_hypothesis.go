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

// ListApprovedHypotheses S2 桥输入：approved 态假设行（trialing 已入册不再返）。
func (s *Store) ListApprovedHypotheses(ctx context.Context) ([]OppHypothesis, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, venue, category_key, COALESCE(inst_type,''), COALESCE(shape,''),
		       COALESCE(family_fit,''), COALESCE(detail,'')
		FROM fin_opp_hypothesis WHERE status='approved' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list approved hypotheses: %w", err)
	}
	defer rows.Close()
	var out []OppHypothesis
	for rows.Next() {
		var h OppHypothesis
		if err := rows.Scan(&h.ID, &h.Venue, &h.CategoryKey, &h.InstType,
			&h.Shape, &h.FamilyFit, &h.Detail); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// MarkHypothesisTrialing approved→trialing（入册完成标记+detail 尾注去向）。
func (s *Store) MarkHypothesisTrialing(ctx context.Context, id int64, sink string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE fin_opp_hypothesis SET status='trialing', detail=detail||' ｜S2桥→'||$2,
		last_seen=now() WHERE id=$1 AND status='approved'`, id, sink)
	if err != nil {
		return fmt.Errorf("mark hypothesis trialing #%d: %w", id, err)
	}
	return nil
}
