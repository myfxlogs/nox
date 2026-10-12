// reconcile_fast_retry.go —— D-407 S1/S2：对冲仓残腿密集重试（裸露窗口压缩）。
// 背景：498 事故——harvest 触发后 MT 腿 orderclose REQUEST_ERROR 瞬时故障，30min 看护
// 阈值对「残腿=单向裸露」过宽（裸多黄金 39min 实损 −$12.36）。本驱动 30s tick 秒级起步
// 重试至平掉或窗口结束，之后静默交还 30min 看护（不双轨）。
// MT 侧探针盲区（Ping/Health/CheckConnect 类探测探不出「能看不能动」降级）——密集重试是
// 唯一正确补救；venue 失败频次计数供「频率太高找备用」判定（备用切换不出票）。
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// fastRetryInterval 密集重试 tick（~30s；窗口内每 tick 扫一轮 CLOSING）。
const fastRetryInterval = 30 * time.Second

// fastRetryWindowCount 10min 窗内累计失败升级阈（venue 维度）。固定常量——SSOT 配置面
// 只列 window/consecutiveN；连续 streak 达阈告警后清零，窗内累计是低频分散失败兜底。
const fastRetryWindowCount = 5

// StartFastRetryLoop D-407 S1：残腿密集重试驱动（main 装配 safego.Go 注册）。
// 阻塞至 ctx 取消。每 tick 一轮 fastRetrySweep。
func (r *Reconciler) StartFastRetryLoop(ctx context.Context) {
	ticker := time.NewTicker(fastRetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.fastRetrySweep(ctx)
			r.resolveUnknownLegs(ctx) // D-421 §1.5：UNKNOWN 消解 30s 同 tick
		}
	}
}

// fastRetrySweep 单轮：扫 CLOSING ∪ LEGGED（D-414 扩域）仓。
// CLOSING 三条件全满足才 ResumeClose——
// ①closing_started_at 距今 < fastRetryWindow（窗外静默交还 30min 看护）
// ②uncertainty_reason 为空（D-189 铁律：不确定单永不自动重试，盲平=反向开新仓）
// ③venue 核验腿仍在由 ResumeClose 内部 checkResidualLegs 自带拦（不重复查）。
// LEGGED 无 closing_started_at——先 ClaimClose（SQL COALESCE 写起算点）上车，
// 之后生命周期落入同一台 CLOSING 重试/看护/告警机器（零新机器只加上车闸）。
// ResumeClose 返 ErrCloseInFlight=另一调用方持锁执行中 → 可重入不告警不计数。
// 返 nil → 清 venue 失败计数；其他 err → venue 计数+达阈升级告警。
func (r *Reconciler) fastRetrySweep(ctx context.Context) {
	if r.closer == nil {
		return
	}
	listCtx, listCancel := r.withTimeout(ctx)
	closings, err := r.store.ListPositions(listCtx, "CLOSING", 50, 0)
	leggeds, lerr := r.store.ListPositions(listCtx, "LEGGED", 50, 0)
	listCancel()
	if err != nil {
		log.Printf("[reconcile] fast retry list CLOSING: %v", err)
	}
	if lerr != nil {
		log.Printf("[reconcile] fast retry list LEGGED: %v", lerr)
	}
	if err != nil && lerr != nil {
		return
	}
	now := r.now()
	for i := range leggeds {
		r.fastRetryLegged(ctx, &leggeds[i])
	}
	for i := range closings {
		p := &closings[i]
		if p.UncertaintyReason != nil && *p.UncertaintyReason != "" {
			continue // ② UNKNOWN 红线
		}
		if p.ClosingStartedAt == nil || now.Sub(*p.ClosingStartedAt) >= r.fastRetryWindow {
			continue // ① 窗外/nil=异常态 → 交还 30min 看护（isClosingStuck 判 stuck）
		}
		r.fastRetryResume(ctx, p.ID)
	}
}

// fastRetryLegged D-414 §1.1：LEGGED 单条上车闸——uncertainty 红线 → residualLegVenues
// 归因 → ClaimClose（ErrClaimFailed=并发已收编仍走 ResumeClose 幂等；其他 err→计数下轮）
// → ResumeClose（尾部与 CLOSING 同口径）。
func (r *Reconciler) fastRetryLegged(ctx context.Context, p *pgstore.Position) {
	if p.UncertaintyReason != nil && *p.UncertaintyReason != "" {
		return // UNKNOWN 红线（LEGGED 语义=另一腿确定未成交；含疑即人工域）
	}
	if !r.preflightLeggedFailedLegs(ctx, p) {
		return // D-421 §1.6：FAILED 腿真实终态未定/实际已成交 → 本轮中止收编
	}
	venues := r.residualLegVenues(ctx, p.ID)
	sub, cancel := r.withTimeout(ctx)
	err := r.closer.ClaimClose(sub, p.ID)
	cancel()
	switch {
	case err == nil, errors.Is(err, pgstore.ErrClaimFailed):
		// 上车成功/已被收编 → 幂等 ResumeClose（首次成功才写 closing_started_at）
	default:
		log.Printf("[reconcile] fast retry legged pos=%d claim: %v", p.ID, err)
		r.fastRetryFail(ctx, venues)
		return
	}
	r.fastRetryResumeVenues(ctx, p.ID, venues)
}

// fastRetryResume CLOSING 分支 ResumeClose+计数尾部（venues 现算）。
func (r *Reconciler) fastRetryResume(ctx context.Context, posID int64) {
	r.fastRetryResumeVenues(ctx, posID, r.residualLegVenues(ctx, posID))
}

// fastRetryResumeVenues ResumeClose 尾部公共段：nil→清计数；ErrCloseInFlight→可重入
// 不告警不计数；其他 err→log+venue 计数。
func (r *Reconciler) fastRetryResumeVenues(ctx context.Context, posID int64, venues []string) {
	sub, cancel := r.withTimeout(ctx)
	err := r.closer.ResumeClose(sub, posID)
	cancel()
	switch {
	case err == nil:
		r.fastRetryClear(venues)
	case errors.Is(err, exec.ErrCloseInFlight):
		// 单飞哨兵：快试/看护/手动三路之一持锁执行中——可重入，不告警不计数
	default:
		log.Printf("[reconcile] fast retry pos=%d failed: %v", posID, err)
		r.fastRetryFail(ctx, venues)
	}
}

// residualLegVenues 该仓 OPEN FILLED 残余腿的 venue 集合（失败计数归因粒度）。
// 腿查询失败 → nil（venue 不明不计数，不瞎归因）。
func (r *Reconciler) residualLegVenues(ctx context.Context, posID int64) []string {
	sub, cancel := r.withTimeout(ctx)
	legs, err := r.store.ListPositionLegs(sub, posID)
	cancel()
	if err != nil {
		log.Printf("[reconcile] fast retry list legs pos=%d: %v", posID, err)
		return nil
	}
	// 同 leg_no 已存在 CLOSE 终态行（FILLED=系统自发平掉 / EXTERNAL=venue 推送
	// 外部已平标记）→ 该腿不再算残余，排除出失败归因与重试面。
	closeDone := make(map[int16]bool)
	for _, l := range legs {
		if l.Phase == "CLOSE" && (l.Status == "FILLED" || l.Status == "EXTERNAL") {
			closeDone[l.LegNo] = true
		}
	}
	set := make(map[string]bool)
	for _, l := range legs {
		if l.Phase == "OPEN" && l.Status == "FILLED" && !closeDone[l.LegNo] {
			set[l.Venue] = true
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	return out
}

// fastRetryFail venue 维度失败计数：连续 streak++ + 窗内时间戳累计；达阈（连续
// consecN 或窗内 ≥fastRetryWindowCount）→ warn 升级告警并清零该 venue 计数
// （告警节流：再达阈才再报，不逐次刷）。
func (r *Reconciler) fastRetryFail(ctx context.Context, venues []string) {
	now := r.now()
	cutoff := now.Add(-r.fastRetryWindow)
	var escalated []string
	r.mu.Lock()
	for _, v := range venues {
		r.fastRetryStreak[v]++
		r.fastRetryTimes[v] = append(r.fastRetryTimes[v], now)
		kept := r.fastRetryTimes[v][:0]
		for _, ts := range r.fastRetryTimes[v] {
			if ts.After(cutoff) {
				kept = append(kept, ts)
			}
		}
		r.fastRetryTimes[v] = kept
		if r.fastRetryStreak[v] >= r.fastRetryConsec || len(r.fastRetryTimes[v]) >= fastRetryWindowCount {
			escalated = append(escalated,
				fmt.Sprintf("venue %s 平仓通道疑似降级，连续 %d 次 / %s 窗内 %d 次失败——备用通道待业主裁",
					v, r.fastRetryStreak[v], r.fastRetryWindow, len(r.fastRetryTimes[v])))
			delete(r.fastRetryStreak, v)
			delete(r.fastRetryTimes, v)
			r.degradedVenues[v] = true // D-015 ①：升级置位降级态（观测/告警；R1 起不入 Gate 开仓判定）
		}
	}
	r.mu.Unlock()
	for _, msg := range escalated {
		r.insertAlertCtx(ctx, "warn", msg)
		log.Printf("[reconcile] %s", msg)
	}
}

// fastRetryClear ResumeClose 成功（含幂等 nil）→ 清该仓残余 venue 的失败计数。
func (r *Reconciler) fastRetryClear(venues []string) {
	if len(venues) == 0 {
		return
	}
	r.mu.Lock()
	for _, v := range venues {
		delete(r.fastRetryStreak, v)
		delete(r.fastRetryTimes, v)
		delete(r.degradedVenues, v) // D-015 ①：平仓成功清零降级态（观测用）
	}
	r.mu.Unlock()
}

// DegradedVenues 当前处于平仓通道降级态的 venue 集合（fastRetry 升级置位、
// ResumeClose 成功清零）。D-015 R1：仅观测/告警用途，不入 Gate 开仓判定——
// 降级标记可能在残腿已平后残存，消费之=永久误拦。
func (r *Reconciler) DegradedVenues() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.degradedVenues))
	for v := range r.degradedVenues {
		out = append(out, v)
	}
	return out
}
