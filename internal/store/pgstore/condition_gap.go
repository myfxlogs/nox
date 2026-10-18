// condition_gap.go —— D-186③：fin_condition_gap 态机+缺口→飞书请求通道
// （复用 InsertAlert→D-168 episode 闸：warn 文案含模板×条件键天然去重；
// requested=已推业主、fulfilled/waived=人工收口）。
package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"nox/internal/platform/alerttext"
)

// OpenConditionGap 缺口登记（UNIQUE(template_id,kind,subject) 幂等：已存在不重复建行，
// 返回行 id）。登记即发请求=OpenConditionGap+RequestConditionGap 两步，
// 供 D-187 匹配引擎先记缺口再按 episode 节奏推。
func (s *Store) OpenConditionGap(ctx context.Context, g *ConditionGap) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO fin_condition_gap (template_id, kind, subject, qualifier, request_text)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (template_id, kind, subject) DO NOTHING
		RETURNING id`,
		g.TemplateID, g.Kind, g.Subject, g.Qualifier, g.RequestText).Scan(&id)
	if err == pgx.ErrNoRows { // 幂等命中：返既有行 id
		err = s.pool.QueryRow(ctx, `
			SELECT id FROM fin_condition_gap
			WHERE template_id=$1 AND kind=$2 AND subject=$3`,
			g.TemplateID, g.Kind, g.Subject).Scan(&id)
	}
	if err != nil {
		return 0, fmt.Errorf("open condition gap: %w", err)
	}
	return id, nil
}

// ListConditionGaps 缺口清单（status 空串=全态）。
func (s *Store) ListConditionGaps(ctx context.Context, status string) ([]ConditionGap, error) {
	q := `SELECT id, template_id, kind, subject, qualifier, request_text, status, created_at
	      FROM fin_condition_gap`
	args := []any{}
	if status != "" {
		q += ` WHERE status=$1`
		args = append(args, status)
	}
	q += ` ORDER BY id`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list condition gaps: %w", err)
	}
	defer rows.Close()
	var out []ConditionGap
	for rows.Next() {
		var g ConditionGap
		if err := rows.Scan(&g.ID, &g.TemplateID, &g.Kind, &g.Subject, &g.Qualifier,
			&g.RequestText, &g.Status, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// gapDispatchTopic D-622 泛化面：业主请求类 kind→转发令主题（角色恒=决策席——请求
// 发起/处置是席位会话动作，业主只喂条件，D-594）。表外 kind（含 runtime=系统侧病灶
// D-196① 非「请」语义）不嵌令——保守缺省合禁线「纯信息/自愈面不加令」，新请求类
// 入表=显式动作。
var gapDispatchTopic = map[string]string{
	"capital":    "资本请求",
	"credential": "凭据/权限请求",
	"permission": "凭据/权限请求",
	"transfer":   "划转请求",
	"executor":   "执行请求",
	"constraint": "条件请求",
	"holding":    "条件请求",
}

// pushGapAlert 缺口→alerts 承载推送（Feishu 链经 alertHook 漏斗；D-168 episode 闸
// 按全文案键去重防抖）。文案=前缀+主体+末行席位转发令（业主请求类含请求行 id 定位
// 请求箱行；RequestConditionGap/ReopenConditionGap 共用，禁第二份组装逻辑）。
func (s *Store) pushGapAlert(ctx context.Context, id int64, g ConditionGap) error {
	prefix := "条件缺口"
	if g.Kind == "runtime" {
		prefix = "系统侧病灶" // D-196①：runtime 行照常推但前缀非「请」
	}
	dispatch := ""
	if topic, ok := gapDispatchTopic[g.Kind]; ok {
		dispatch = alerttext.SeatDispatchLine("决策",
			fmt.Sprintf("%s处置（读 STATE 本行+fin_condition_gap#%d）", topic, id))
	}
	return s.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
		"%s %s %s:%s —— %s%s", prefix, g.TemplateID, g.Kind, g.Subject, g.RequestText, dispatch))
}

// RequestConditionGap 缺口→飞书请求：status→requested+requested_at，
// InsertAlert warn 承载推送（D-168 episode 闸按文案键去重防抖——同缺口再推不炸屏）。
// D-196②：复推闸 open→requested 扩为 open|fulfilled→requested——fulfilled 行
// 条件再失后同 key 缺口撞 fulfilled 键也能复推（与 D-193 reopen 语义对齐）。
func (s *Store) RequestConditionGap(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE fin_condition_gap SET status='requested', requested_at=now(),
		       resolved_at=NULL, resolve_note=NULL
		WHERE id=$1 AND status IN ('open','fulfilled')`, id)
	if err != nil {
		return fmt.Errorf("request condition gap: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil // 已 requested/收口=幂等零推
	}
	var g ConditionGap
	if err := s.pool.QueryRow(ctx, `
		SELECT template_id, kind, subject, request_text
		FROM fin_condition_gap WHERE id=$1`, id).
		Scan(&g.TemplateID, &g.Kind, &g.Subject, &g.RequestText); err != nil {
		return err
	}
	return s.pushGapAlert(ctx, id, g)
}

// FulfillConditionGap D-193② 自动收口：open/requested→fulfilled+resolve_note
// （条件满足自动收口 / 「需求已变更」收口——条件行被删场景）。
func (s *Store) FulfillConditionGap(ctx context.Context, id int64, note string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE fin_condition_gap SET status='fulfilled', resolved_at=now(), resolve_note=$2
		WHERE id=$1 AND status IN ('open','requested')`, id, note)
	return err
}

// ReopenConditionGap D-193② 反向跳变：fulfilled 行条件再失→回 requested
// （同 key 复用不插新行；告警再推走 D-168 episode 闸防抖照旧）。
func (s *Store) ReopenConditionGap(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE fin_condition_gap SET status='requested', requested_at=now(), resolved_at=NULL
		WHERE id=$1 AND status='fulfilled'`, id)
	if err != nil {
		return fmt.Errorf("reopen condition gap: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	var g ConditionGap
	if err := s.pool.QueryRow(ctx, `
		SELECT template_id, kind, subject, request_text
		FROM fin_condition_gap WHERE id=$1`, id).
		Scan(&g.TemplateID, &g.Kind, &g.Subject, &g.RequestText); err != nil {
		return err
	}
	return s.pushGapAlert(ctx, id, g)
}

// ResolveConditionGap 收口（fulfilled=业主已喂条件 / waived=放弃该机会线；
// note=回执留痕如「投资人页回执」——D-268 M4.6 补参）。
// applied=false=行已收口/不存在（幂等——双击/重发不炸）。
func (s *Store) ResolveConditionGap(ctx context.Context, id int64, fulfilled bool, note string) (bool, error) {
	st := "waived"
	if fulfilled {
		st = "fulfilled"
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE fin_condition_gap SET status=$2, resolved_at=now(), resolve_note=$3
		WHERE id=$1 AND status IN ('open','requested')`, id, st, note)
	return tag.RowsAffected() > 0, err
}

// RequestBoxRow 投资人页请求箱行（requested 集+请求龄——D-268）。
type RequestBoxRow struct {
	ConditionGap
	RequestedAt *time.Time
}

// ListRequestedGaps 请求箱集：status='requested' 按请求龄最老在前
// （§4.3 指标④最老请求 age 同源口径）。
func (s *Store) ListRequestedGaps(ctx context.Context) ([]RequestBoxRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, template_id, kind, subject, qualifier,
		request_text, status, created_at, requested_at
		FROM fin_condition_gap WHERE status='requested'
		ORDER BY COALESCE(requested_at, created_at) ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RequestBoxRow
	for rows.Next() {
		var r RequestBoxRow
		if err := rows.Scan(&r.ID, &r.TemplateID, &r.Kind, &r.Subject, &r.Qualifier,
			&r.RequestText, &r.Status, &r.CreatedAt, &r.RequestedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
