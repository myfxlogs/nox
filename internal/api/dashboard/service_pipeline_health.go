// service_pipeline_health.go —— S3′-b 十段工序健康读模（业主令：
// 每段=状态点+一行实况叙事；dept 台账与工序非一一对应，禁前端拼装）。
// 判据单源：每段一处探针（dephealth 注册表/DB 现值列/既有 pendingEntries
// 组装），探针读败=error 如实不吞、无凭据段=idle 不造 working 象。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"nox/internal/gen/dashboardv1"
	"nox/internal/infra/dephealth"
)

// stage 段构建器产出（行组装收口点——各段函数各自判态，统一收口）。
type stageRow = dashboardv1.PipelineStageHealth

func stageNow(key, state, narrative, wait, errStr string, last time.Time) *stageRow {
	r := &stageRow{Key: key, State: state, Narrative: narrative, WaitReason: wait, Error: errStr}
	r.EvidenceKind = stageEvidence[key]
	if !last.IsZero() {
		r.LastEventUnix = last.Unix()
	}
	return r
}

// stageEvidence 段级 liveness 证据分型（S3′-c）：beat=周期心跳（last_event
// 超龄=断流嫌疑该转 waiting）；event=事件驱动（零事件=如实 idle 非病态）；
// none=无簿（凭据缺如实标）。判据各段自填——本表是消费面单源。
// 注：ledger/review 归 event——日频业务事件时戳非 worker 心跳，自带
// 48h/7d 业务节奏阈（stageLedger/stageReview），15min beat 缝会误伤。
// beatStaleWindow beat 断流判定阈（看门狗级非业务数据刷——D-004 心
// 跳检测面）。
const beatStaleWindow = 15 * time.Minute

// applyBeatStaleness 超龄降级缝（S3′-c 返修抽纯函数便锚测）：beat 型段
// last_event 超窗且 working → waiting「心跳断流嫌疑」；超龄 last_event=0
// （从未拍）不翻——如实「未记账」非断流。
func applyBeatStaleness(rows []*stageRow, now time.Time) {
	for _, r := range rows {
		if r.EvidenceKind == "beat" && r.State == "working" && r.LastEventUnix > 0 &&
			now.Sub(time.Unix(r.LastEventUnix, 0)) > beatStaleWindow {
			r.State = "waiting"
			r.WaitReason = "心跳超龄（beat 断流嫌疑）"
		}
	}
}

var stageEvidence = map[string]string{
	"collect": "beat", "research": "event", "opp_publish": "event",
	"gate_eval": "beat", "assembly": "event", "execution": "event",
	"holding": "event", "exit": "event", "ledger": "event", "review": "event",
}

// pipelineStages 十段工序健康行（序=闭环序：采集→研究→机会发布→闸评→装配→
// 执行→持仓→平仓→账本→复盘）。每段一处探针，无探针段=idle 如实「未建簿」。
func (s *Service) pipelineStages(ctx context.Context) []*stageRow {
	rows := []*stageRow{
		s.stageCollect(ctx), s.stageResearch(ctx), s.stageOppPublish(ctx),
		s.stageGateEval(ctx), s.stageAssembly(ctx), s.stageExecution(ctx),
		s.stageHolding(ctx), s.stageExit(ctx), s.stageLedger(ctx), s.stageReview(ctx),
	}
	applyBeatStaleness(rows, time.Now())
	return rows
}

// ① 采集：dephealth 注册表聚合——err 键存在=故障（首键原文透出），ok 键=工作，
// 无行=未建簿 idle。venue_online 状态走同一注册表面不另判据。
func (s *Service) stageCollect(_ context.Context) *stageRow {
	var okN int
	var firstErr dephealth.Row
	var last time.Time
	var suspect dephealth.Row
	for _, d := range dephealth.Snapshot() {
		// 跨段键过滤：engine:/scan:/gate_eval: 等非采集键染红采集卡=错段
		// 归责——采集段只聚合采集面键（venue/ws/egress 族）。
		if strings.HasPrefix(d.Key, "engine:") || strings.HasPrefix(d.Key, "scan:") ||
			strings.HasPrefix(d.Key, "gate_eval:") {
			continue
		}
		if d.LastEventUnix > last.Unix() {
			last = time.Unix(d.LastEventUnix, 0)
		}
		switch d.State {
		case dephealth.StateHealthy:
			okN++
		case dephealth.StateDegraded:
			if firstErr.Key == "" {
				firstErr = d
			}
		case dephealth.StateSuspect:
			if suspect.Key == "" {
				suspect = d
			}
		}
	}
	if firstErr.Key != "" {
		return stageNow("collect", "error", fmt.Sprintf("依赖 %s 断供", firstErr.Key), "", firstErr.State, last)
	}
	if suspect.Key != "" {
		return stageNow("collect", "waiting", fmt.Sprintf("%d 路依赖在通", okN), fmt.Sprintf("%s 连败未达阈（suspect）", suspect.Key), "", last)
	}
	if okN > 0 {
		return stageNow("collect", "working", fmt.Sprintf("%d 路依赖在通", okN), "", "", last)
	}
	return stageNow("collect", "idle", "采集依赖未记账", "", "", last)
}

// ② 研究：carry_graduations pending/unqualified 在评行 + 最近提案时戳。
func (s *Service) stageResearch(ctx context.Context) *stageRow {
	var n int
	var last time.Time
	err := s.store.Pool().QueryRow(ctx,
		`SELECT count(*), COALESCE(max(created_at),'epoch') FROM carry_graduations WHERE status IN ('pending','unqualified')`).Scan(&n, &last)
	if err != nil {
		return stageNow("research", "error", "研究池读败", "", err.Error(), time.Time{})
	}
	if n > 0 {
		return stageNow("research", "working", fmt.Sprintf("%d 纸面行在评/观察", n), "", "", last)
	}
	return stageNow("research", "idle", "零在评毕业行", "", "", last)
}

// ③ 机会发布：opportunities 最近行——24h 内有发=工作；超龄=等待（引擎评估中）。
func (s *Service) stageOppPublish(ctx context.Context) *stageRow {
	var last *time.Time
	err := s.store.Pool().QueryRow(ctx,
		`SELECT max(created_at) FROM opportunities`).Scan(&last)
	if err != nil {
		return stageNow("opp_publish", "error", "机会簿读败", "", err.Error(), time.Time{})
	}
	if last == nil {
		return stageNow("opp_publish", "idle", "零已发机会", "", "", time.Time{})
	}
	if time.Since(*last) < 24*time.Hour {
		return stageNow("opp_publish", "working", fmt.Sprintf("最近发布 %s", last.UTC().Format("01-02 15:04Z")), "", "", *last)
	}
	return stageNow("opp_publish", "waiting", "超 24h 零新发", fmt.Sprintf("上次发布 %s", last.UTC().Format("01-02 15:04Z")), "", *last)
}

// ④ 闸评：pendingEntries 组装件复用——行数+首行 wait_reason 透出（单源不另判）。
func (s *Service) stageGateEval(ctx context.Context) *stageRow {
	rows, err := s.pendingEntries(ctx)
	if err != nil {
		return stageNow("gate_eval", "error", "待入场区组装败", "", err.Error(), time.Time{})
	}
	if len(rows) == 0 {
		return stageNow("gate_eval", "idle", "零 armed 组合在评", "", "", time.Time{})
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
	// S3′-c：beat 键真时戳（engine:opp 每帧记账——假 time.Now() 摘除）
	var last time.Time
	// S3′-c 返修：beat 源=watch 环每拍重扫（engine:opp 是事件帧非周期环——
	// 静默期假断流病灶）。engine:opp Mark 保留作机会消费面证据不作闸评源。
	for _, d := range dephealth.Snapshot() {
		if d.Key == "gate_eval:watch" && d.LastEventUnix > 0 {
			last = time.Unix(d.LastEventUnix, 0)
		}
	}
	return stageNow("gate_eval", st, fmt.Sprintf("%d 组合在评", len(rows)), wait, "", last)
}

// ⑤ 装配：executor dephealth 键 + 缺资条件缺口——故障键/缺口原文透出。
func (s *Service) stageAssembly(ctx context.Context) *stageRow {
	for _, d := range dephealth.Snapshot() {
		// dephealth 状态域=healthy/suspect/degraded/idle/unknown——故障面=degraded。
		if d.State == dephealth.StateDegraded {
			return stageNow("assembly", "error", fmt.Sprintf("装配径 %s 故障", d.Key), "", d.State, time.Unix(d.LastEventUnix, 0))
		}
	}
	var n int
	if err := s.store.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_condition_gap WHERE status='requested'`).Scan(&n); err == nil && n > 0 {
		return stageNow("assembly", "waiting", "缺资条件挂业主", fmt.Sprintf("%d 条件请求待回", n), "", time.Now())
	}
	return stageNow("assembly", "working", "装配径无故障记账", "", "", time.Now())
}

// ⑥ 执行：24h 内开过仓=工作；否则等待（armed 等闸放行）。
func (s *Service) stageExecution(ctx context.Context) *stageRow {
	var last *time.Time
	err := s.store.Pool().QueryRow(ctx,
		`SELECT max(opened_at) FROM positions`).Scan(&last)
	if err != nil {
		return stageNow("execution", "error", "执行簿读败", "", err.Error(), time.Time{})
	}
	if last == nil {
		return stageNow("execution", "idle", "零开仓记录", "", "", time.Time{})
	}
	if time.Since(*last) < 24*time.Hour {
		return stageNow("execution", "working", fmt.Sprintf("最近开仓 %s", last.UTC().Format("01-02 15:04Z")), "", "", *last)
	}
	return stageNow("execution", "waiting", "等 armed 放行", fmt.Sprintf("上次开仓 %s", last.UTC().Format("01-02 15:04Z")), "", *last)
}

// ⑦ 持仓：在仓行数直透。
func (s *Service) stageHolding(ctx context.Context) *stageRow {
	var n int
	err := s.store.Pool().QueryRow(ctx,
		`SELECT count(*) FROM positions WHERE status='ACTIVE'`).Scan(&n)
	if err != nil {
		return stageNow("holding", "error", "持仓簿读败", "", err.Error(), time.Time{})
	}
	if n > 0 {
		return stageNow("holding", "working", fmt.Sprintf("%d 仓在持", n), "", "", time.Now())
	}
	return stageNow("holding", "idle", "零在持仓", "", "", time.Time{})
}

// ⑧ 平仓：最近 closed_at 透出（未平过=idle）。
func (s *Service) stageExit(ctx context.Context) *stageRow {
	var last *time.Time
	err := s.store.Pool().QueryRow(ctx,
		`SELECT max(closed_at) FROM positions WHERE status='CLOSED'`).Scan(&last)
	if err != nil {
		return stageNow("exit", "error", "平仓簿读败", "", err.Error(), time.Time{})
	}
	if last == nil {
		return stageNow("exit", "idle", "零平仓记录", "", "", time.Time{})
	}
	return stageNow("exit", "working", fmt.Sprintf("最近平仓 %s", last.UTC().Format("01-02 15:04Z")), "", "", *last)
}

// ⑨ 账本：position_daily_income 最近行日——账簿节拍活性。
func (s *Service) stageLedger(ctx context.Context) *stageRow {
	var last *time.Time
	err := s.store.Pool().QueryRow(ctx,
		`SELECT max(day::timestamptz) FROM position_daily_income`).Scan(&last)
	if err != nil {
		return stageNow("ledger", "error", "账簿读败", "", err.Error(), time.Time{})
	}
	if last == nil {
		return stageNow("ledger", "idle", "零日收益行", "", "", time.Time{})
	}
	if time.Since(*last) < 48*time.Hour {
		return stageNow("ledger", "working", fmt.Sprintf("账面到 %s", last.Format("01-02")), "", "", *last)
	}
	return stageNow("ledger", "waiting", "账行超 48h 未更", "", "", *last)
}

// ⑩ 复盘：fin_dept_reports 最近 report_date——部门复盘节拍。
func (s *Service) stageReview(ctx context.Context) *stageRow {
	var last *time.Time
	err := s.store.Pool().QueryRow(ctx,
		`SELECT max(report_date::timestamptz) FROM fin_dept_reports`).Scan(&last)
	if err != nil {
		return stageNow("review", "error", "复盘簿读败", "", err.Error(), time.Time{})
	}
	if last == nil {
		return stageNow("review", "idle", "零复盘行", "", "", time.Time{})
	}
	if time.Since(*last) < 7*24*time.Hour {
		return stageNow("review", "working", fmt.Sprintf("最近复盘 %s", last.Format("01-02")), "", "", *last)
	}
	return stageNow("review", "waiting", "复盘超 7d 未更", "", "", *last)
}
