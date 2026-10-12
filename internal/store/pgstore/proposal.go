// proposal.go —— D-189：提案实体化 fin_proposal。
// 状态机：proposed→executed|expired|declined（decided_at 同写）；
// 幂等=同模板 proposed 行在册即复用其稳定 id（日扫不重开单——#N 跨轮稳定）。
package pgstore

import (
	"context"

	"encoding/json"
	"nox/internal/data/keydomain"
	"strings"
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

// DormantPairs D-204①：休眠轨机器可读集——subject 'dormant:<tmpl>@<venue>' 前缀匹配
// +value_num=1 生效 → "tmpl@venue" 键集（生产种子 id=10 已在账；零=非休眠）。
func DormantPairs(cons []CapabilityConstraint) map[string]bool {
	out := map[string]bool{}
	for _, c := range cons {
		if c.ValueNum != nil && *c.ValueNum == 1 && strings.HasPrefix(c.Subject, "dormant:") {
			out[strings.TrimPrefix(c.Subject, "dormant:")] = true
		}
	}
	return out
}

// DeferProposalsByPair D-204②：存量 proposed 行翻 deferred（休眠轨挂起——
// declined=人拒/expired=失配过期语义皆错；CAS 仅 proposed 态动）。
func (s *Store) DeferProposalsByPair(ctx context.Context, template, venue string) (int, error) {
	res, err := s.pool.Exec(ctx, `
		UPDATE fin_proposal SET status='deferred', decided_at=now()
		WHERE status='proposed' AND template_id=$1 AND venue=$2`, template, venue)
	if err != nil {
		return 0, err
	}
	return int(res.RowsAffected()), nil
}

// XAssetDormantKey D-209：XASSET_CARRY 休眠键粒度=canonical×venue（非 venue 对）——
// canonical 先剥 USD/USDT 报价后缀取金属键（XAUUSD→XAU、XAGUSDT→XAG），
// venue 取裸端点（keydomain.BareOf 复合键归一）。例：xasset_carry:XAU@okx。
func XAssetDormantKey(canonical, venue string) string {
	metal := strings.ToUpper(canonical)
	for _, suf := range []string{"USDT", "USD"} {
		metal = strings.TrimSuffix(metal, suf)
	}
	return "xasset_carry:" + metal + "@" + keydomain.BareOf(venue)
}

// XAssetDormantHit 消费点判——cons 约束行集（调用方 ListCapabilityConstraints 热读）
// 内休眠前缀查双腿 venue 各一次；命中返回休眠键（台账/拒因文案携键）。
func XAssetDormantHit(cons []CapabilityConstraint, canonical string, venues ...string) (string, bool) {
	d := DormantPairs(cons)
	for _, v := range venues {
		if k := XAssetDormantKey(canonical, v); d[k] {
			return k, true
		}
	}
	return "", false
}
