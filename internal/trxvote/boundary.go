// D-137 R1：改投调度事件驱动化——reward_num_sun 差分即维护期边界探测器
// （实证：rewardNum 12/12 次变化都在 00/06/12/18 UTC±90s 内、vote_claim 全贴边界）。
// 30min 盲醒 tick 摘除；改投评估只在三类事件后发生：
//
//	① reward 差分非零（trx_account 30s 采集搭车探测，延迟 ≤30s）
//	② 自发 tx 确认（vote/claim 广播后有界确认窗）
//	③ 本地 failsafe/boot 补课（零奖励周期边界兜底——纯本地比对，稳态零外部调用）
package trxvote

import (
	"context"
	"log"
	"time"

	"arb/internal/safego"
)

// maintenancePeriod TRON SR 维护期=6h（00/06/12/18 UTC 边界，实证±90s 结算）。
const maintenancePeriod = 6 * time.Hour

// boundaryGrace 边界宽限——rewardNum 结算在边界后 ±90s 内落地；failsafe 醒在边界+宽限后才算漏过。
const boundaryGrace = 3 * time.Minute

// confirmWindowSteps 自发 tx 有界确认窗：~19s 内 3 次回执（4s/8s/7s 错位等块产出）。
var confirmWindowSteps = []time.Duration{4 * time.Second, 8 * time.Second, 7 * time.Second}

// KickBoundary 外部事件触发改投评估（reward 差分/自 tx 确认等）。非阻塞：已排队则合并。
func (s *Scheduler) KickBoundary(reason string) {
	select {
	case s.boundaryCh <- reason:
	default:
	}
}

// SetNextMaintenanceSource 注入 GetNextMaintenanceTime 源（failsafe/boot 补课用，gRPC 一次调用）。
func (s *Scheduler) SetNextMaintenanceSource(fn func(ctx context.Context) (time.Time, error)) {
	s.nextMaintFn = fn
}

// SetOnEvaluated 注入评估完成回调（边界事件 → apr.TriggerNow+publishHedge，wire 层接线）。
func (s *Scheduler) SetOnEvaluated(fn func(ctx context.Context)) { s.onEvaluated = fn }

// SetOnSelfTxConfirmed 注入自发 tx 确认回调（confirmed → account/apr 重取，wire 层接线）。
func (s *Scheduler) SetOnSelfTxConfirmed(fn func(ctx context.Context)) { s.onSelfTxConfirmed = fn }

// SetFailsafeEvery 设置 failsafe 核醒间隔（测试可注小值，默认 60s——纯本地比对，零外部调用）。
func (s *Scheduler) SetFailsafeEvery(d time.Duration) { s.failsafeEvery = d }

// SetConfirmSteps 替换确认窗节奏（测试用，默认 confirmWindowSteps）。
func (s *Scheduler) SetConfirmSteps(steps []time.Duration) { s.confirmSteps = steps }

// eventLoop 事件驱动改投评估循环（替代 D-293 的 30min 盲醒 ticker）。
// 三触发源：boundaryCh（reward 差分 kick）/failsafe ticker（本地比对缓存 nextMaint）/boot 补课。
func (s *Scheduler) eventLoop(ctx context.Context) {
	s.bootCatchup(ctx)
	every := s.failsafeEvery
	if every <= 0 {
		every = 60 * time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case reason := <-s.boundaryCh:
			log.Printf("[trxvote] boundary event (%s) → rebalance eval", reason)
			s.doRebalance(ctx)
			s.noteEvaluated(ctx)
		case <-ticker.C:
			s.failsafeCheck(ctx)
		}
	}
}

// bootCatchup 启动补课：上次评估时刻 < 上次维护期边界 → 进程停机期漏过边界，补评估一次。
// 只补一次（评估会落 decided_at，重进不再补）。
func (s *Scheduler) bootCatchup(ctx context.Context) {
	next, ok := s.refreshNextMaintenance(ctx)
	if !ok {
		return
	}
	lastBoundary := next.Add(-maintenancePeriod)
	lastEval, err := s.store.LastRebalanceDecidedAt(ctx)
	if err != nil {
		lastEval = time.Time{} // 查询失败保守按「无评估记录」处理→漏过才补
	}
	if lastEval.Before(lastBoundary) {
		log.Printf("[trxvote] boot catch-up: lastEval=%v < lastBoundary=%v → 补评估一次",
			lastEval.Format(time.RFC3339), lastBoundary.Format(time.RFC3339))
		s.doRebalance(ctx)
		s.noteEvaluated(ctx)
	}
}

// failsafeCheck 零奖励周期兜底：now > 缓存 nextMaint+宽限 且 边界未被 reward 差分消化 → 补评估。
// 纯本地比对（稳态零外部调用）；触发后刷新 nextMaint（此刻一次外部调用）。
func (s *Scheduler) failsafeCheck(ctx context.Context) {
	s.nextMaintMu.Lock()
	next := s.nextMaint
	s.nextMaintMu.Unlock()
	if next.IsZero() {
		s.refreshNextMaintenance(ctx) // 尚无缓存→拉一次（非周期轮询，只填缓存）
		return
	}
	if s.now().Before(next.Add(boundaryGrace)) {
		return // 未过边界+宽限——安静窗不动作
	}
	log.Printf("[trxvote] failsafe: boundary %v passed +%v grace without reward delta → eval",
		next.Format(time.RFC3339), boundaryGrace)
	s.doRebalance(ctx)
	s.noteEvaluated(ctx)
	s.refreshNextMaintenance(ctx) // 消费掉此边界，滚入下一周期
}

// refreshNextMaintenance 拉取并缓存下一维护期边界。返回 (next, ok)。
func (s *Scheduler) refreshNextMaintenance(ctx context.Context) (time.Time, bool) {
	if s.nextMaintFn == nil {
		return time.Time{}, false
	}
	next, err := s.nextMaintFn(ctx)
	if err != nil {
		log.Printf("[trxvote] get next maintenance time: %v", err)
		return time.Time{}, false
	}
	s.nextMaintMu.Lock()
	s.nextMaint = next
	s.nextMaintMu.Unlock()
	return next, true
}

// noteEvaluated 评估后记：本地滚动消费当前边界（nextMaint 按 6h 周期前滚至 now+宽限之后，
// 零外部调用）→ reward-delta/确认窗评估后 failsafe 不误补；onEvaluated 回调。
func (s *Scheduler) noteEvaluated(ctx context.Context) {
	s.nextMaintMu.Lock()
	for !s.nextMaint.IsZero() && !s.nextMaint.Add(boundaryGrace).After(s.now()) {
		s.nextMaint = s.nextMaint.Add(maintenancePeriod)
	}
	s.nextMaintMu.Unlock()
	if s.onEvaluated != nil {
		s.onEvaluated(ctx)
	}
}

// confirmSelfTx 自发 tx 有界确认窗：广播后 ~19s 内 3 次回执，confirmed → onSelfTxConfirmed。
// 有界即合法——非无限轮询；全部未确认只 log（下轮 trx_txs 低频对账仍会拾到）。
func (s *Scheduler) confirmSelfTx(ctx context.Context, txHash string) {
	steps := s.confirmSteps
	if len(steps) == 0 {
		steps = confirmWindowSteps
	}
	for i, d := range steps {
		timer := time.NewTimer(d)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		ok, err := s.executor.TxConfirmed(ctx, txHash)
		if err == nil && ok {
			log.Printf("[trxvote] self tx %s confirmed (attempt %d)", txHash, i+1)
			if s.onSelfTxConfirmed != nil {
				s.onSelfTxConfirmed(ctx)
			}
			return
		}
	}
	log.Printf("[trxvote] self tx %s not confirmed within %vs window (deferred to trx_txs reconcile)", txHash, 19)
}

// watchSelfTx 广播成功后起确认窗（safego panic 护栏）。
func (s *Scheduler) watchSelfTx(ctx context.Context, txHash string) {
	safego.Go("trxvote:confirmSelfTx", func() { s.confirmSelfTx(ctx, txHash) })
}
