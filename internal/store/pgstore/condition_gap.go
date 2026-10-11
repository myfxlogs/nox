// condition_gap.go —— D-186③：fin_condition_gap 态机+缺口→飞书请求通道
// （复用 InsertAlert→D-168 episode 闸：warn 文案含模板×条件键天然去重；
// requested=已推业主、fulfilled/waived=人工收口）。
package pgstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
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
	q := `SELECT id, template_id, kind, subject, qualifier, request_text, status
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
			&g.RequestText, &g.Status); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RequestConditionGap 缺口→飞书请求：status→requested+requested_at，
// InsertAlert warn 承载推送（D-168 episode 闸按文案键去重防抖——同缺口再推不炸屏）。
func (s *Store) RequestConditionGap(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE fin_condition_gap SET status='requested', requested_at=now()
		WHERE id=$1 AND status='open'`, id)
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
	return s.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
		"条件缺口 %s %s:%s —— %s", g.TemplateID, g.Kind, g.Subject, g.RequestText))
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
	return s.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
		"条件缺口 %s %s:%s —— %s", g.TemplateID, g.Kind, g.Subject, g.RequestText))
}

// ResolveConditionGap 收口（fulfilled=业主已喂条件 / waived=放弃该机会线）。
func (s *Store) ResolveConditionGap(ctx context.Context, id int64, fulfilled bool) error {
	st := "waived"
	if fulfilled {
		st = "fulfilled"
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE fin_condition_gap SET status=$2, resolved_at=now()
		WHERE id=$1 AND status IN ('open','requested')`, id, st)
	return err
}
