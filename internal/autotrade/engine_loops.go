// engine_loops.go —— positionLoop / quoteKickDispatch。
// D-004 P1#4 重构：退出评估分层——quote 帧纯内存快路（命中才升单仓权威评估）；
// 订单事件/carry facts/到点边界 → 权威全量 scanAndClose；fallback ticker 退化
// 为饥荒看门狗（只告警不评估）。旧「30s 节流丢弃 + 10min 兜底评估」废除。
package autotrade

import (
	"context"
	"log"
	"time"
)

// positionFallbackInterval D-107 P4→D-004 P1#4：现为退出评估饥荒看门狗周期旧值（10min）。
// D-378a S3 起默认 60s 配置化（ARB_POSITION_EVAL_SEC→cfg.PositionFallbackInterval）；
// 本常量保留为 ≤0 防呆回退值。包级 var 测试可覆写（marginReconnectBackoff 先例）。
var positionFallbackInterval = 10 * time.Minute

// fallbackInterval D-004 P1#4：饥荒看门狗周期——有仓、久未评估且报价在流
// （事件链断了）→ 告警；不携带、不刷新业务数据（定时器唯一合法形态）。
func (e *Engine) fallbackInterval() time.Duration {
	if e.cfg.PositionFallbackInterval > 0 {
		return e.cfg.PositionFallbackInterval
	}
	return positionFallbackInterval
}

// Kick 订单事件触发重扫（非阻塞 send 到 buffered(1) chan；positionLoop 侧 1s 合流不丢事件）。
func (e *Engine) Kick() {
	select {
	case e.kickCh <- struct{}{}:
	default: // 已有待处理 kick，合并
	}
}

// positionLoop D-004 P1#4：启动即扫+装缓存 → select 事件集：
//   - quoteCh：纯内存 quoteExitHit（零 DB）；命中且距该仓上次权威评估 ≥1s →
//     evaluatePosition 单仓升权威（1s 是命中态持续的合流，不是轮询）；
//   - kickCh（订单事件）：≥kickCoalesce(1s) 立即全扫，窗口内置 pendingKick 由
//     合流定时器补跑——事件不丢（废除旧 30s 丢弃语义）；
//   - carryKick：carry facts 写后全扫（息差型输入变了）；
//   - 到点定时器：probe 到期/PERP_FUNDING 持有下限的最早未来边界 +1s；
//   - watchdog：饥荒看门狗——有仓、久未评估且报价在流 → 告警，不评估；
//   - csMissTick：D-140 S2 CS-miss 聚合告警（不动）。
//
// open/lastScan/pendingKick/边界定时器均 positionLoop 单协程读写（lastScan 先例）。
func (e *Engine) positionLoop(ctx context.Context) {
	e.scanAndClose(ctx)
	e.stateMachineTick(ctx)
	lastEval := time.Now()
	lastScan := lastEval
	open := e.reloadOpen(ctx)
	lastQuoteSeen := time.Time{}

	watchdog := time.NewTicker(e.fallbackInterval())
	defer watchdog.Stop()
	csMissTick := time.NewTicker(csMissAlertInterval)
	defer csMissTick.Stop()

	var boundaryT *time.Timer
	var boundaryC <-chan time.Time
	armBoundary := func() {
		if boundaryT != nil {
			boundaryT.Stop()
			boundaryT, boundaryC = nil, nil
		}
		if t, ok := e.nextExitBoundary(open); ok {
			boundaryT = time.NewTimer(time.Until(t) + time.Second) // 到点边界 +1s
			boundaryC = boundaryT.C
		}
	}
	armBoundary()

	pendingKick := false
	var coalesceT *time.Timer
	var coalesceC <-chan time.Time
	armCoalesce := func() {
		if coalesceT == nil {
			d := kickCoalesce - time.Since(lastScan)
			if d < 0 {
				d = 0
			}
			coalesceT = time.NewTimer(d)
			coalesceC = coalesceT.C
		}
	}

	runFull := func() { // 权威全扫 + 缓存重载 + 边界重算
		e.scanAndClose(ctx)
		e.stateMachineTick(ctx)
		now := time.Now()
		lastScan, lastEval, pendingKick = now, now, false
		open = e.reloadOpen(ctx)
		armBoundary()
	}

	for {
		select {
		case <-ctx.Done():
			if coalesceT != nil {
				coalesceT.Stop()
			}
			if boundaryT != nil {
				boundaryT.Stop()
			}
			return
		case q := <-e.quoteCh:
			lastQuoteSeen = time.Now()
			for i := range open {
				if open[i].canonical == "" || open[i].canonical != q.Symbol {
					continue
				}
				if !e.quoteExitHit(&open[i]) {
					continue
				}
				if time.Since(open[i].lastAuth) < authMinInterval {
					continue // 命中态持续合流（非轮询）
				}
				open[i].lastAuth = time.Now()
				lastEval = time.Now()
				if e.evaluatePosition(ctx, &open[i].pos) {
					open = e.reloadOpen(ctx)
					armBoundary()
				}
			}
		case <-e.kickCh:
			if time.Since(lastScan) >= kickCoalesce {
				runFull()
			} else {
				pendingKick = true
				armCoalesce()
			}
		case <-coalesceC:
			coalesceT, coalesceC = nil, nil
			if pendingKick {
				runFull()
			}
		case <-e.carryKick:
			runFull()
		case <-boundaryC:
			boundaryT, boundaryC = nil, nil
			runFull()
		case <-watchdog.C:
			// 饥荒看门狗（D-004 P1#4）：只告警——有仓、评估久未跑、报价仍在流 = 事件链断了。
			if len(open) > 0 && time.Since(lastEval) > e.fallbackInterval() && !lastQuoteSeen.IsZero() && time.Since(lastQuoteSeen) < e.fallbackInterval() {
				msg := "exit-eval starvation: positionLoop 有持仓且报价在流但权威评估超时未跑"
				log.Printf("[autotrade] WATCHDOG %s (open=%d lastEval=%v ago lastQuote=%v ago)",
					msg, len(open), time.Since(lastEval).Round(time.Second), time.Since(lastQuoteSeen).Round(time.Second))
				if e.AlertSink != nil {
					e.AlertSink("critical", "退出评估饥荒看门狗："+msg)
				}
			}
		case <-csMissTick.C:
			e.flushCSMissAlerts() // D-140 S2
		}
	}
}

// quoteKickDispatch D-469c S1/S2→D-004 P1#4：qbus 全订 → positionLoop 送 quoteCh
// 帧（满则丢，报价热路径不阻塞）；canonical ∈ MeanRevertThresh 时另 send mrKick
// （meanRevert 入场路径不变）。
func (e *Engine) quoteKickDispatch(ctx context.Context) {
	ch, cancel := e.quotes.Subscribe("")
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case q, ok := <-ch:
			if !ok {
				return
			}
			select {
			case e.quoteCh <- q:
			default: // 快路满 → 丢帧（下一帧还会来）
			}
			if _, tracked := e.cfg.MeanRevertThresh[q.Symbol]; tracked {
				e.KickMeanRevert()
			}
		}
	}
}
