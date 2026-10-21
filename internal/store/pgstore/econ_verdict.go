// econ_verdict.go —— 经济经验库消化裁定（消化链建制票②）：三分出路枚举落库。
// canon=转正条目（fixpoint 回改点必填——无回改点不入库）/dismiss=理由必填驳回
// （同因重报=病灶信号，负结算接线命中已裁键→warn 告警）/merge=并入既有条目。
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// EconVerdict 裁定行（econ_kb_verdicts，迁移 1065）。
type EconVerdict struct {
	EventKey  string
	Verdict   string // canon|dismiss|merge
	Reason    string
	EntryID   string // canon=新条目 id；merge=并入目标 id；dismiss=空
	Fixpoint  string // canon 必填：eval|kill_trigger|combo_gate|sizing|semantic:<target>
	DecidedBy string
	DecidedAt time.Time
}

// ErrEconVerdictExists 同键已裁定（幂等撞键——重复裁定须先删旧行非覆盖）。
var ErrEconVerdictExists = errors.New("econ verdict exists")

// InsertEconVerdict 落裁定。入参校验前置（同 CHECK 语义，错误更可读）：
// 理由必填（dismiss 垃圾桶防线——同因重报=病灶信号非静默吞）；canon/merge
// 须带 entry_id；canon 另须 fixpoint 回改点（无回改点不入库票面钉死）。
func (s *Store) InsertEconVerdict(ctx context.Context, v EconVerdict) error {
	if strings.TrimSpace(v.EventKey) == "" {
		return fmt.Errorf("econ verdict: event_key 空")
	}
	switch v.Verdict {
	case "canon", "merge":
		if strings.TrimSpace(v.EntryID) == "" {
			return fmt.Errorf("econ verdict: %s 须带 entry_id", v.Verdict)
		}
		if v.Verdict == "canon" && strings.TrimSpace(v.Fixpoint) == "" {
			return fmt.Errorf("econ verdict: canon 须带 fixpoint 回改点（无回改点不入库）")
		}
	case "dismiss":
	default:
		return fmt.Errorf("econ verdict: 未知裁定 %q（域=canon|dismiss|merge）", v.Verdict)
	}
	if strings.TrimSpace(v.Reason) == "" {
		return fmt.Errorf("econ verdict: 理由必填（dismiss 防垃圾桶——同因重报=病灶信号）")
	}
	var eid, fp any
	if v.EntryID != "" {
		eid = v.EntryID
	}
	if v.Fixpoint != "" {
		fp = v.Fixpoint
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO econ_kb_verdicts (event_key, verdict, reason, entry_id, fixpoint, decided_by)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		v.EventKey, v.Verdict, v.Reason, eid, fp, v.DecidedBy)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrEconVerdictExists
		}
		return fmt.Errorf("insert econ verdict: %w", err)
	}
	return nil
}

// EconVerdictByKey 单键裁定查询（负结算接线幂等消费点）。ok=false=未裁。
func (s *Store) EconVerdictByKey(ctx context.Context, key string) (EconVerdict, bool, error) {
	var v EconVerdict
	var eid, fp *string
	err := s.pool.QueryRow(ctx, `
		SELECT event_key, verdict, reason, entry_id, fixpoint, decided_by, decided_at
		FROM econ_kb_verdicts WHERE event_key=$1`, key).
		Scan(&v.EventKey, &v.Verdict, &v.Reason, &eid, &fp, &v.DecidedBy, &v.DecidedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return EconVerdict{}, false, nil
		}
		return EconVerdict{}, false, err
	}
	if eid != nil {
		v.EntryID = *eid
	}
	if fp != nil {
		v.Fixpoint = *fp
	}
	return v, true, nil
}

// EconVerdictDist 窗口裁定分布（周报消化态列——retro.go 查询件）。
func (s *Store) EconVerdictDist(ctx context.Context, since, until time.Time) (map[string]int, error) {
	out := map[string]int{}
	rows, err := s.pool.Query(ctx, `
		SELECT verdict, count(*) FROM econ_kb_verdicts
		WHERE decided_at >= $1 AND decided_at < $2 GROUP BY verdict`, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// EconPendingBacklog 未裁定积压：负结算机械产线行中尚无裁定者
// （position_settlements.net<0 ∧ 非事故 ∧ key 非空 ∧ 裁定表无行）。
// 返回行数+最老行 settled_at 至今天数（周报消化态列 + digest 积压闸共用源）。
func (s *Store) EconPendingBacklog(ctx context.Context) (rows int, oldestDays int, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT count(*),
		       COALESCE(EXTRACT(EPOCH FROM (now()-min(settled_at)))/86400,0)::int
		FROM position_settlements s
		WHERE s.net_realized_usd < 0 AND s.incident_attributed = false
		  AND s.settlement_event_key IS NOT NULL AND s.settlement_event_key <> ''
		  AND NOT EXISTS (SELECT 1 FROM econ_kb_verdicts v WHERE v.event_key = s.settlement_event_key)`,
	).Scan(&rows, &oldestDays)
	if err != nil {
		return 0, 0, fmt.Errorf("econ pending backlog: %w", err)
	}
	return rows, oldestDays, nil
}
