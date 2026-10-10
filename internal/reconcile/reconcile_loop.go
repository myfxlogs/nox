package reconcile

import (
	"context"
	"log"
	"time"

	"arb/internal/store/pgstore"
)

// reconKickCoalesce kick 合流窗口（D-004 P1#6）：多腿开仓会连发订单事件，
// REST 对账不能每事件一轮——窗口外即到即跑，窗口内置 pending 定时补跑，事件不丢。
const reconKickCoalesce = 2 * time.Second

// Kick 对账事件叫醒（非阻塞，buffered(1) 合并）。
// 事件源：订单事件（orderTopic）/ positions 表写点（posChange）/ 流重连（OnReconnect）。
func (r *Reconciler) Kick() {
	select {
	case r.kickCh <- struct{}{}:
	default:
	}
}

// StartEventLoop 事件驱动对账循环（D-004 P1#6，替代 5min 定期 ticker）。阻塞至 ctx 取消。
// 触发源：
//   - boot：initialDelay 后首跑（等 adapter 就绪，原缺陷2语义保留）；首跑前的 kick 只置
//     pending，首跑后由合流定时器补跑一次。
//   - kick：reconKickCoalesce 合流（见上）。
//   - 到点边界：OPENING/LEGGED 僵尸 24h 阈值、CLOSING 卡住 closingStuckTimeout /
//     告警分层 closingTierWarnAfter，单定时器到点全量 Run 后重算。
//
// 裁定：无周期 ticker、无看门狗——静市「久未跑」是常态，没有可判定的饥荒条件；
// venue 断流期漏掉的事件由各流重连成功的 OnReconnect → Kick 兜住。
func (r *Reconciler) StartEventLoop(ctx context.Context) {
	var (
		ready   bool
		pending bool
		lastRun time.Time
		coalC   <-chan time.Time
		boundC  <-chan time.Time
	)
	coalT := newStoppedTimer()
	boundT := newStoppedTimer()
	defer coalT.Stop()
	defer boundT.Stop()

	armBoundary := func() {
		resetTimer(boundT, 0)
		if t, ok := r.nextBoundary(ctx); ok {
			boundT.Reset(time.Until(t))
			boundC = boundT.C
		} else {
			boundC = nil
		}
	}
	run := func() {
		if _, err := r.Run(ctx); err != nil {
			log.Printf("[reconcile] run: %v", err)
		}
		lastRun = r.now()
		ready = true
		armBoundary()
	}

	initialT := time.NewTimer(r.initialDelay)
	defer initialT.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-initialT.C:
			run()
			if pending { // 欠的补跑挂合流定时器（pending 保留至补跑时清）
				coalT.Reset(reconKickCoalesce)
				coalC = coalT.C
			}
		case <-r.kickCh:
			if !ready {
				pending = true
				continue
			}
			if since := r.now().Sub(lastRun); since >= reconKickCoalesce {
				run()
			} else {
				pending = true
				if coalC == nil {
					coalT.Reset(reconKickCoalesce - since)
					coalC = coalT.C
				}
			}
		case <-coalC:
			coalC = nil
			if pending {
				pending = false
				run()
			}
		case <-boundC:
			boundC = nil
			run()
		}
	}
}

// nextBoundary 下一个对账到点边界（Run 的业务截止时刻单源）：
//   - OPENING/LEGGED：opened_at+zombieAge（无 opened_at 无未来边界——已在场僵尸
//     由事件 kick 兜住，nil 时间戳不表）。
//   - CLOSING：closing_started_at+closingStuckTimeout（ResumeClose 重触发点）与
//     +closingTierWarnAfter（告警降层点）。
//
// 返回最早的未来边界 +1s；无边界 ok=false（调用方停表）。
// 各 ListPositions 失败只记日志跳过该状态——边界缺省不阻断事件驱动主路。
func (r *Reconciler) nextBoundary(ctx context.Context) (time.Time, bool) {
	now := r.now()
	var next time.Time
	mark := func(t time.Time) {
		if t.After(now) && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	list := func(status string) []pgstore.Position {
		sub, cancel := r.withTimeout(ctx)
		ps, err := r.store.ListPositions(sub, status, 50, 0)
		cancel()
		if err != nil {
			log.Printf("[reconcile] nextBoundary list %s: %v", status, err)
			return nil
		}
		return ps
	}
	for _, p := range list("OPENING") {
		if p.OpenedAt != nil {
			mark(p.OpenedAt.Add(zombieAge))
		}
	}
	for _, p := range list("LEGGED") {
		if p.OpenedAt != nil {
			mark(p.OpenedAt.Add(zombieAge))
		}
	}
	for _, p := range list("CLOSING") {
		if p.ClosingStartedAt != nil {
			mark(p.ClosingStartedAt.Add(closingStuckTimeout))
			mark(p.ClosingStartedAt.Add(closingTierWarnAfter))
		}
	}
	// D-175：LEGGED 裸露延迟判定到期点（逾期需宣判 critical，不能等 kick 兜底）。
	if t, ok := r.leggedDeferralBoundary(ctx); ok {
		mark(t)
	}
	if next.IsZero() {
		return time.Time{}, false
	}
	return next.Add(time.Second), true
}

func newStoppedTimer() *time.Timer {
	t := time.NewTimer(time.Hour)
	if !t.Stop() {
		<-t.C
	}
	return t
}

// resetTimer 停表排空后 Reset（d<=0 表示仅停表不排程）。
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	if d > 0 {
		t.Reset(d)
	}
}
