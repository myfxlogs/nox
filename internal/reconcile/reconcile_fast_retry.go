// reconcile_fast_retry.go —— D-407 S1/S2：对冲仓残腿密集重试（裸露窗口压缩）。
// 背景：498 事故——harvest 触发后 MT 腿 orderclose REQUEST_ERROR 瞬时故障，30min 看护
// 阈值对「残腿=单向裸露」过宽（裸多黄金 39min 实损 −$12.36）。本驱动 30s tick 秒级起步
// 重试至平掉或窗口结束，之后静默交还 30min 看护（不双轨）。
// MetaApi 探针盲区（Ping/Health/CheckConnect 探不出「能看不能动」降级）——密集重试是
// 唯一正确补救；venue 失败频次计数供「频率太高找备用」判定（备用切换不出票）。
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"arb/internal/exec"
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
		}
	}
}

// fastRetrySweep 单轮：扫 CLOSING 仓，三条件全满足才 ResumeClose——
// ①closing_started_at 距今 < fastRetryWindow（窗外静默交还 30min 看护）
// ②uncertainty_reason 为空（D-189 铁律：不确定单永不自动重试，盲平=反向开新仓）
// ③venue 核验腿仍在由 ResumeClose 内部 checkResidualLegs 自带拦（不重复查）。
// ResumeClose 返 ErrCloseInFlight=另一调用方持锁执行中 → 可重入不告警不计数。
// 返 nil → 清 venue 失败计数；其他 err → venue 计数+达阈升级告警。
func (r *Reconciler) fastRetrySweep(ctx context.Context) {
	if r.closer == nil {
		return
	}
	listCtx, listCancel := r.withTimeout(ctx)
	closings, err := r.store.ListPositions(listCtx, "CLOSING", 50, 0)
	listCancel()
	if err != nil {
		log.Printf("[reconcile] fast retry list CLOSING: %v", err)
		return
	}
	now := r.now()
	for i := range closings {
		p := &closings[i]
		if p.UncertaintyReason != nil && *p.UncertaintyReason != "" {
			continue // ② UNKNOWN 红线
		}
		if p.ClosingStartedAt == nil || now.Sub(*p.ClosingStartedAt) >= r.fastRetryWindow {
			continue // ① 窗外/nil=异常态 → 交还 30min 看护（isClosingStuck 判 stuck）
		}
		venues := r.residualLegVenues(ctx, p.ID)
		sub, cancel := r.withTimeout(ctx)
		err := r.closer.ResumeClose(sub, p.ID)
		cancel()
		switch {
		case err == nil:
			r.fastRetryClear(venues)
		case errors.Is(err, exec.ErrCloseInFlight):
			// 单飞哨兵：快试/看护/手动三路之一持锁执行中——可重入，不告警不计数
		default:
			log.Printf("[reconcile] fast retry pos=%d failed: %v", p.ID, err)
			r.fastRetryFail(ctx, venues)
		}
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
	set := make(map[string]bool)
	for _, l := range legs {
		if l.Phase == "OPEN" && l.Status == "FILLED" {
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
	}
	r.mu.Unlock()
}
