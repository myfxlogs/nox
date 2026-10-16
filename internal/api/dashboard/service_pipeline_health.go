// service_pipeline_health.go —— S3′-b 十段工序健康读模（业主令：
// 每段=状态点+一行实况叙事；dept 台账与工序非一一对应，禁前端拼装）。
// 判据单源：每段一处探针（dephealth 注册表/DB 现值列/既有 pendingEntries
// 组装），探针读败=error 如实不吞、无凭据段=idle 不造 working 象。
package dashboard

import (
	"context"
	"fmt"
	"time"

	"nox/internal/gen/dashboardv1"
	"nox/internal/infra/dephealth"
)

// stage 段构建器产出（行组装收口点——各段函数各自判态，统一收口）。
type stageRow = dashboardv1.PipelineStageHealth

func stageNow(state, narrative, wait, errStr string, last time.Time) *stageRow {
	r := &stageRow{State: state, Narrative: narrative, WaitReason: wait, Error: errStr}
	if !last.IsZero() {
		r.LastEventUnix = last.Unix()
	}
	return r
}

// pipelineStages 十段工序健康行（序=闭环序：采集→研究→机会发布→闸评→装配→
// 执行→持仓→平仓→账本→复盘）。每段一处探针，无探针段=idle 如实「未建簿」。
func (s *Service) pipelineStages(ctx context.Context) []*stageRow {
	return []*stageRow{
		s.stageCollect(ctx), s.stageResearch(ctx), s.stageOppPublish(ctx),
		s.stageGateEval(ctx), s.stageAssembly(ctx), s.stageExecution(ctx),
		s.stageHolding(ctx), s.stageExit(ctx), s.stageLedger(ctx), s.stageReview(ctx),
	}
}

// ① 采集：dephealth 注册表聚合——err 键存在=故障（首键原文透出），ok 键=工作，
// 无行=未建簿 idle。venue_online 状态走同一注册表面不另判据。
func (s *Service) stageCollect(_ context.Context) *stageRow {
	var okN int
	var firstErr dephealth.Row
	var last time.Time
	for _, d := range dephealth.Snapshot() {
		if d.LastEventUnix > last.Unix() {
			last = time.Unix(d.LastEventUnix, 0)
		}
		if d.State == "ok" || d.State == "live" || d.State == "online" {
			okN++
			continue
		}
		if firstErr.Key == "" && (d.State == "error" || d.State == "down" || d.State == "degraded") {
			firstErr = d
		}
	}
	if firstErr.Key != "" {
		return stageNow("error", fmt.Sprintf("依赖 %s 断供", firstErr.Key), "", firstErr.State, last)
	}
	if okN > 0 {
		return stageNow("working", fmt.Sprintf("%d 路依赖在通", okN), "", "", last)
	}
	return stageNow("idle", "采集依赖未记账", "", "", last)
}

// ② 研究：carry_graduations pending/unqualified 在评行 + 最近提案时戳。
func (s *Service) stageResearch(ctx context.Context) *stageRow {
	var n int
	var last time.Time
	err := s.store.Pool().QueryRow(ctx,
		`SELECT count(*), COALESCE(max(created_at),'epoch') FROM carry_graduations WHERE status IN ('pending','unqualified')`).Scan(&n, &last)
	if err != nil {
		return stageNow("error", "研究池读败", "", err.Error(), time.Time{})
	}
	if n > 0 {
		return stageNow("working", fmt.Sprintf("%d 纸面行在评/观察", n), "", "", last)
	}
	return stageNow("idle", "零在评毕业行", "", "", last)
}

// ③ 机会发布：opportunities 最近行——24h 内有发=工作；超龄=等待（引擎评估中）。
func (s *Service) stageOppPublish(ctx context.Context) *stageRow {
	var last *time.Time
	err := s.store.Pool().QueryRow(ctx,
		`SELECT max(created_at) FROM opportunities`).Scan(&last)
	if err != nil {
		return stageNow("error", "机会簿读败", "", err.Error(), time.Time{})
	}
	if last == nil {
		return stageNow("idle", "零已发机会", "", "", time.Time{})
	}
	if time.Since(*last) < 24*time.Hour {
		return stageNow("working", fmt.Sprintf("最近发布 %s", last.UTC().Format("01-02 15:04Z")), "", "", *last)
	}
	return stageNow("waiting", "超 24h 零新发", fmt.Sprintf("上次发布 %s", last.UTC().Format("01-02 15:04Z")), "", *last)
}

// ④ 闸评：pendingEntries 组装件复用——行数+首行 wait_reason 透出（单源不另判）。
func (s *Service) stageGateEval(ctx context.Context) *stageRow {
	rows, err := s.pendingEntries(ctx)
	if err != nil {
		return stageNow("error", "待入场区组装败", "", err.Error(), time.Time{})
	}
	if len(rows) == 0 {
		return stageNow("idle", "零 armed 组合在评", "", "", time.Time{})
	}
	wait := ""
	for _, r := range rows {
		if !r.Ready {
			wait = r.WaitReason
			break
		}
	}
	st := "working"
	if wait != "" {
		st = "waiting"
	}
	return stageNow(st, fmt.Sprintf("%d 组合在评", len(rows)), wait, "", time.Now())
}

// ⑤ 装配：executor dephealth 键 + 缺资条件缺口——故障键/缺口原文透出。
func (s *Service) stageAssembly(ctx context.Context) *stageRow {
	for _, d := range dephealth.Snapshot() {
		if d.State == "error" || d.State == "down" {
			return stageNow("error", fmt.Sprintf("装配径 %s 故障", d.Key), "", d.State, time.Unix(d.LastEventUnix, 0))
		}
	}
	var n int
	if err := s.store.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_condition_gap WHERE status='requested'`).Scan(&n); err == nil && n > 0 {
		return stageNow("waiting", "缺资条件挂业主", fmt.Sprintf("%d 条件请求待回", n), "", time.Now())
	}
	return stageNow("working", "装配径无故障记账", "", "", time.Now())
}

// ⑥ 执行：24h 内开过仓=工作；否则等待（armed 等闸放行）。
func (s *Service) stageExecution(ctx context.Context) *stageRow {
	var last *time.Time
	err := s.store.Pool().QueryRow(ctx,
		`SELECT max(opened_at) FROM positions`).Scan(&last)
	if err != nil {
		return stageNow("error", "执行簿读败", "", err.Error(), time.Time{})
	}
	if last == nil {
		return stageNow("idle", "零开仓记录", "", "", time.Time{})
	}
	if time.Since(*last) < 24*time.Hour {
		return stageNow("working", fmt.Sprintf("最近开仓 %s", last.UTC().Format("01-02 15:04Z")), "", "", *last)
	}
	return stageNow("waiting", "等 armed 放行", fmt.Sprintf("上次开仓 %s", last.UTC().Format("01-02 15:04Z")), "", *last)
}

// ⑦ 持仓：在仓行数直透。
func (s *Service) stageHolding(ctx context.Context) *stageRow {
	var n int
	err := s.store.Pool().QueryRow(ctx,
		`SELECT count(*) FROM positions WHERE status='ACTIVE'`).Scan(&n)
	if err != nil {
		return stageNow("error", "持仓簿读败", "", err.Error(), time.Time{})
	}
	if n > 0 {
		return stageNow("working", fmt.Sprintf("%d 仓在持", n), "", "", time.Now())
	}
	return stageNow("idle", "零在持仓", "", "", time.Time{})
}

// ⑧ 平仓：最近 closed_at 透出（未平过=idle）。
func (s *Service) stageExit(ctx context.Context) *stageRow {
	var last *time.Time
	err := s.store.Pool().QueryRow(ctx,
		`SELECT max(closed_at) FROM positions WHERE status='CLOSED'`).Scan(&last)
	if err != nil {
		return stageNow("error", "平仓簿读败", "", err.Error(), time.Time{})
	}
	if last == nil {
		return stageNow("idle", "零平仓记录", "", "", time.Time{})
	}
	return stageNow("working", fmt.Sprintf("最近平仓 %s", last.UTC().Format("01-02 15:04Z")), "", "", *last)
}

// ⑨ 账本：position_daily_income 最近行日——账簿节拍活性。
func (s *Service) stageLedger(ctx context.Context) *stageRow {
	var last *time.Time
	err := s.store.Pool().QueryRow(ctx,
		`SELECT max(day::timestamptz) FROM position_daily_income`).Scan(&last)
	if err != nil {
		return stageNow("error", "账簿读败", "", err.Error(), time.Time{})
	}
	if last == nil {
		return stageNow("idle", "零日收益行", "", "", time.Time{})
	}
	if time.Since(*last) < 48*time.Hour {
		return stageNow("working", fmt.Sprintf("账面到 %s", last.Format("01-02")), "", "", *last)
	}
	return stageNow("waiting", "账行超 48h 未更", "", "", *last)
}

// ⑩ 复盘：fin_dept_reports 最近 report_date——部门复盘节拍。
func (s *Service) stageReview(ctx context.Context) *stageRow {
	var last *time.Time
	err := s.store.Pool().QueryRow(ctx,
		`SELECT max(report_date::timestamptz) FROM fin_dept_reports`).Scan(&last)
	if err != nil {
		return stageNow("error", "复盘簿读败", "", err.Error(), time.Time{})
	}
	if last == nil {
		return stageNow("idle", "零复盘行", "", "", time.Time{})
	}
	if time.Since(*last) < 7*24*time.Hour {
		return stageNow("working", fmt.Sprintf("最近复盘 %s", last.Format("01-02")), "", "", *last)
	}
	return stageNow("waiting", "复盘超 7d 未更", "", "", *last)
}
