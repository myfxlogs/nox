// proposal.go —— D-189：提案实体化 fin_proposal。
// 状态机：proposed→executed|expired|declined（decided_at 同写）；
// 幂等=同模板 proposed 行在册即复用其稳定 id（日扫不重开单——#N 跨轮稳定）。
package pgstore

import (
	"context"
	"encoding/json"
	"time"
)

// Proposal 提案行（params=快照审计锚，执行时实时闸复验——快照非钉价）。
type Proposal struct {
	ID          int64
	TemplateID  string
	ExecTrack   string
	Venue       string
	Params      map[string]any
	ExpectedAPR *float64
	Status      string
	CreatedAt   time.Time
	DecidedAt   *time.Time
}

// CreateProposal 提案落行：同模板已有 proposed 行→复用其 id（幂等防重开）。
func (s *Store) CreateProposal(ctx context.Context, p *Proposal) (int64, bool, error) {
	pj, _ := json.Marshal(p.Params)
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO fin_proposal (template_id, exec_track, venue, params, expected_apr)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT DO NOTHING RETURNING id`,
		p.TemplateID, p.ExecTrack, p.Venue, pj, p.ExpectedAPR).Scan(&id)
	if err != nil {
		// 已落同模板 proposed 行→返其稳定 id。
		err2 := s.pool.QueryRow(ctx, `
			SELECT id FROM fin_proposal
			WHERE template_id=$1 AND status='proposed'
			ORDER BY id DESC LIMIT 1`, p.TemplateID).Scan(&id)
		if err2 != nil {
			return 0, false, err
		}
		return id, false, nil
	}
	return id, true, nil
}

// ListProposals 提案列表（status 空=全部，id 降序限量）。
func (s *Store) ListProposals(ctx context.Context, status string, limit int) ([]Proposal, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, template_id, exec_track, venue, params, expected_apr,
		       status, created_at, decided_at
		FROM fin_proposal WHERE ($1='' OR status=$1)
		ORDER BY id DESC LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		var p Proposal
		var pj []byte
		if err := rows.Scan(&p.ID, &p.TemplateID, &p.ExecTrack, &p.Venue, &pj,
			&p.ExpectedAPR, &p.Status, &p.CreatedAt, &p.DecidedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(pj, &p.Params)
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProposal 单查。
func (s *Store) GetProposal(ctx context.Context, id int64) (*Proposal, error) {
	var p Proposal
	var pj []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, template_id, exec_track, venue, params, expected_apr,
		       status, created_at, decided_at
		FROM fin_proposal WHERE id=$1`, id).Scan(
		&p.ID, &p.TemplateID, &p.ExecTrack, &p.Venue, &pj,
		&p.ExpectedAPR, &p.Status, &p.CreatedAt, &p.DecidedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(pj, &p.Params)
	return &p, nil
}

// LatestPendingProposal 最新 proposed 且模板属可执行集（裸 go→此映射）。
func (s *Store) LatestPendingProposal(ctx context.Context, templates []string) (*Proposal, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, template_id, exec_track, venue, params, expected_apr,
		       status, created_at, decided_at
		FROM fin_proposal
		WHERE status='proposed' AND template_id = ANY($1)
		ORDER BY id DESC LIMIT 1`, templates)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	var p Proposal
	var pj []byte
	if err := rows.Scan(&p.ID, &p.TemplateID, &p.ExecTrack, &p.Venue, &pj,
		&p.ExpectedAPR, &p.Status, &p.CreatedAt, &p.DecidedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(pj, &p.Params)
	return &p, nil
}

// SetProposalStatus 态机推进（proposed→终态同写 decided_at；CAS 防双判）。
func (s *Store) SetProposalStatus(ctx context.Context, id int64, status string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE fin_proposal SET status=$2, decided_at=now()
		WHERE id=$1 AND status='proposed'`, id, status)
	return err
}

// ExpireProposalsExcept 过期并入扫描拍：当轮不再达标的 pending 提案→expired
// （事件驱动零新定时器；just-executed/declined 行不动）。
func (s *Store) ExpireProposalsExcept(ctx context.Context, liveTemplates []string) (int, error) {
	q := `UPDATE fin_proposal SET status='expired', decided_at=now() WHERE status='proposed'`
	args := []any{}
	if len(liveTemplates) > 0 { // 空集=全过期；ANY(NULL) 谓词恒 NULL 会零行更新
		q += ` AND NOT (template_id = ANY($1))`
		args = append(args, liveTemplates)
	}
	res, err := s.pool.Exec(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return int(res.RowsAffected()), nil
}
