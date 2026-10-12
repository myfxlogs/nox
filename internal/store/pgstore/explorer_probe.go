// explorer_probe.go —— D-048：LLM 探针提案（scan_kind='probe'）持久化面。
// 契约字段全存 probe JSONB（写入前 explorer 校验层已逐字段拒缺）；
// 状态机 proposed→approved→running→closed/rejected 全部 CAS 流转
// （UPDATE ... WHERE status=$from，防并发/重试双执行）。
package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNoProbeProposal probe 提案行不存在（ApproveProbe/RejectProbe NotFound 判据）。
var ErrNoProbeProposal = errors.New("explorer_proposals probe row not found")

// ProbeContract 探针提案契约（migration 0176 probe JSONB 列；写入方 explorer
// 校验层保证全字段非空——读侧仍按空值 fail-closed 防手工插行绕过）。
type ProbeContract struct {
	Purpose         string    `json:"purpose"`
	Falsify         string    `json:"falsify"`
	VenueKey        string    `json:"venue_key"`
	Canonical       string    `json:"canonical"`
	Side            string    `json:"side"`
	Qty             float64   `json:"qty"`
	NotionalUSD     float64   `json:"notional_usd"`
	ExpectedCostUSD float64   `json:"expected_cost_usd"`
	AutoCloseAt     time.Time `json:"auto_close_at"`
	Basis           string    `json:"basis"`
	// Premises D-176：结构化前提（机审唯一判据源；omitempty——旧行零值=无机审判据）。
	Premises []ProbePremise `json:"premises,omitempty"`
}

// ProbePremise 结构化前提一条（kind 决定使用哪些类型字段，与 proto Premise 对齐）。
type ProbePremise struct {
	Kind      string  `json:"kind"`
	Venue     string  `json:"venue,omitempty"`
	Canonical string  `json:"canonical,omitempty"`
	FreeUSD   float64 `json:"free_usd,omitempty"`
}

// ProbeProposal explorer_proposals 探针行（审批/报告读面）。
type ProbeProposal struct {
	ID          int64
	ScanDate    time.Time
	Canonical   string // 列冗余（dedupe 键成分；= Contract.Canonical）
	Status      string
	Contract    ProbeContract
	DecisionRef string // 批准 D#（proposed 态空）
	PositionID  *int64 // probe_position_id（running/closed 关联仓）
	GateNote    string // 拒绝理由/审批注记
}

// ExplorerProbeProposalInsert 落一行探针提案（scan_kind='probe'，status='proposed'）。
// 返回 (id, inserted)：dedupe 唯一键冲突 → (0,false,nil)=同日同 canonical+venue 重复提案跳过。
// canonical 写列、venue_key 进 best_pair 列（复用 dedupe 索引防同日重复推飞书）。
func (s *Store) ExplorerProbeProposalInsert(ctx context.Context, c ProbeContract, scanDate time.Time) (int64, bool, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return 0, false, fmt.Errorf("probe contract marshal: %w", err)
	}
	var id int64
	err = s.pool.QueryRow(ctx, `
		INSERT INTO explorer_proposals
			(scan_date, scan_kind, canonical, best_pair, direction, rationale, probe, status)
		VALUES ($1,'probe',$2,$3,$4,$5,$6,'proposed')
		ON CONFLICT (scan_date, scan_kind, canonical, best_pair) DO NOTHING
		RETURNING id`,
		scanDate, c.Canonical, c.VenueKey, c.Side, c.Purpose, raw).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil // dedupe 命中——幂等跳过
		}
		return 0, false, fmt.Errorf("probe proposal insert: %w", err)
	}
	return id, true, nil
}

// ExplorerProbeProposalGet 按 id 读提案（必须 scan_kind='probe'；否则 ErrNoProbeProposal——
// daily/weekly 提案不可走探针审批面）。
func (s *Store) ExplorerProbeProposalGet(ctx context.Context, id int64) (*ProbeProposal, error) {
	return s.scanProbeProposal(s.pool.QueryRow(ctx, `
		SELECT id, scan_date, canonical, status, probe, COALESCE(decision_ref,''),
		       probe_position_id, COALESCE(gate_note,'')
		FROM explorer_proposals WHERE id=$1 AND scan_kind='probe'`, id))
}

// ExplorerProbeProposalByPosition 按关联仓 id 读提案（平仓报告找假设源；无关联→ErrNoProbeProposal）。
func (s *Store) ExplorerProbeProposalByPosition(ctx context.Context, posID int64) (*ProbeProposal, error) {
	return s.scanProbeProposal(s.pool.QueryRow(ctx, `
		SELECT id, scan_date, canonical, status, probe, COALESCE(decision_ref,''),
		       probe_position_id, COALESCE(gate_note,'')
		FROM explorer_proposals WHERE probe_position_id=$1 AND scan_kind='probe'`, posID))
}

// ExplorerProbeProposalList 按状态列探针提案（scan_kind='probe'，id 升序——先提先审）。
func (s *Store) ExplorerProbeProposalList(ctx context.Context, status string) ([]*ProbeProposal, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, scan_date, canonical, status, probe, COALESCE(decision_ref,''),
		       probe_position_id, COALESCE(gate_note,'')
		FROM explorer_proposals WHERE scan_kind='probe' AND status=$1 ORDER BY id`, status)
	if err != nil {
		return nil, fmt.Errorf("list probe proposals: %w", err)
	}
	defer rows.Close()
	var out []*ProbeProposal
	for rows.Next() {
		p, err := s.scanProbeProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProbeProposalGateNote 记 gate_note（不改状态——机审判据留痕/模糊件幂等键用）。
func (s *Store) ProbeProposalGateNote(ctx context.Context, id int64, note string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE explorer_proposals SET gate_note=$2, updated_at=now()
		WHERE id=$1 AND scan_kind='probe'`, id, note)
	return err
}

func (s *Store) scanProbeProposal(row pgx.Row) (*ProbeProposal, error) {
	var p ProbeProposal
	var raw []byte
	err := row.Scan(&p.ID, &p.ScanDate, &p.Canonical, &p.Status, &raw,
		&p.DecisionRef, &p.PositionID, &p.GateNote)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoProbeProposal
		}
		return nil, fmt.Errorf("probe proposal scan: %w", err)
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p.Contract); err != nil {
			return nil, fmt.Errorf("probe proposal #%d contract decode: %w", p.ID, err)
		}
	}
	return &p, nil
}

// probeProposalCAS 状态原子流转（UPDATE ... WHERE status=$from RETURNING 行数）。
// 并发/重试下只有一次 CAS 成功——ApproveProbe 「OpenProbe 恰好调一次」的持久层保证。
func (s *Store) probeProposalCAS(ctx context.Context, q string, args ...any) (bool, error) {
	tag, err := s.pool.Exec(ctx, q, args...)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ProbeProposalApprove CAS proposed→approved + 记 decision_ref。
func (s *Store) ProbeProposalApprove(ctx context.Context, id int64, ref string) (bool, error) {
	return s.probeProposalCAS(ctx, `
		UPDATE explorer_proposals SET status='approved', decision_ref=$2, updated_at=now()
		WHERE id=$1 AND status='proposed'`, id, ref)
}

// ProbeProposalRun CAS approved→running + 记关联仓。
func (s *Store) ProbeProposalRun(ctx context.Context, id, posID int64) (bool, error) {
	return s.probeProposalCAS(ctx, `
		UPDATE explorer_proposals SET status='running', probe_position_id=$2, updated_at=now()
		WHERE id=$1 AND status='approved'`, id, posID)
}

// ProbeProposalReset CAS approved→proposed（OpenProbe 拒绝回退；decision_ref 清掉等重批）。
func (s *Store) ProbeProposalReset(ctx context.Context, id int64) (bool, error) {
	return s.probeProposalCAS(ctx, `
		UPDATE explorer_proposals SET status='proposed', decision_ref=NULL, updated_at=now()
		WHERE id=$1 AND status='approved'`, id)
}

// ProbeProposalReject CAS proposed→rejected + 理由落 gate_note。
func (s *Store) ProbeProposalReject(ctx context.Context, id int64, reason string) (bool, error) {
	return s.probeProposalCAS(ctx, `
		UPDATE explorer_proposals SET status='rejected', gate_note=$2, updated_at=now()
		WHERE id=$1 AND status='proposed'`, id, reason)
}

// ProbeProposalCloseByPosition running→closed（平仓收尾时同步；无 running 行=静默零行）。
func (s *Store) ProbeProposalCloseByPosition(ctx context.Context, posID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE explorer_proposals SET status='closed', updated_at=now()
		WHERE probe_position_id=$1 AND scan_kind='probe' AND status='running'`, posID)
	return err
}
