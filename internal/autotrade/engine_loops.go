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
	e.lastKickSent.Store(time.Now().UnixNano()) // D-131：事件源侧记账（orderTopic 订阅 goroutine）
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
//   - （D-131：饥荒看门狗改独立 goroutine 外部观测，见 exitEvalWatchdog）；
//   - csMissTick：D-140 S2 CS-miss 聚合告警（不动）。
//
// open/lastScan/pendingKick/边界定时器均 positionLoop 单协程读写（lastScan 先例）。
func (e *Engine) positionLoop(ctx context.Context) {
	e.scanAndClose(ctx)
	e.stateMachineTick(ctx)
	lastEval := time.Now()
	e.lastEvalAt.Store(lastEval.UnixNano())
	lastScan := lastEval
	open := e.reloadOpen(ctx, nil)
	e.openCount.Store(int64(len(open)))

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
		e.lastEvalAt.Store(now.UnixNano())
		open = e.reloadOpen(ctx, open)
		e.openCount.Store(int64(len(open)))
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
				e.lastEvalAt.Store(time.Now().UnixNano())
				if e.evaluatePosition(ctx, &open[i].pos) {
					open = e.reloadOpen(ctx, open)
					e.openCount.Store(int64(len(open)))
					armBoundary()
					// range 遍历的是旧切片：reload 后 open 已换——继续用旧索引会
					// 越界（新切片更短 panic）或评错仓（等长错位）。同帧其余命中
					// 仓由下一帧再评（帧流连续，无损）。
					break
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
			// D-004 P1#5：武装 canonical → swapArb quote 快路（满丢帧不阻塞）。
			if e.swapArmedCanonical(q.Symbol) {
				select {
				case e.swapQuoteCh <- q:
				default:
				}
			}
			if _, tracked := e.cfg.MeanRevertThresh[q.Symbol]; tracked {
				e.KickMeanRevert()
			}
		}
	}
}

// exitEvalWatchdog D-131：饥荒看门狗外部观测版（独立 goroutine——lastKickSent/lastEvalAt
// 由 select 原子服务两侧的 goroutine 分别写，同协程内 kick→eval 永同步=哑弹根因根除）。
// 判据=有持仓 ∧「kick 送达而 loop 超时未消化」（lastKickSent > lastEval+interval）=
// 真链断（loop 卡死/chan 堵）；安静窗无新事件不告警——旧判据「久未评估+报价在流」把
// 驱动节奏（fundingEmitThrottle=60s）与阈值同阶的正常空窗误报为链断。
// 只告警不评估（看门狗不产数据原则不动）；评估触发分层零改动。
func (e *Engine) exitEvalWatchdog(ctx context.Context) {
	t := time.NewTicker(e.fallbackInterval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		open, lastEval, lastKick, starved := e.exitEvalStarved()
		if !starved {
			continue // 无仓 / 尚无事件 / 事件已被消化 = 不告警
		}
		msg := "exit-eval starvation: positionLoop 有持仓且 kick 送达但权威评估超时未消化"
		log.Printf("[autotrade] WATCHDOG %s (open=%d lastEval=%v ago lastKick=%v ago)",
			msg, open, time.Since(lastEval).Round(time.Second), time.Since(lastKick).Round(time.Second))
		if e.AlertSink != nil {
			e.AlertSink("critical", "退出评估饥荒看门狗："+msg)
		}
	}
}

// exitEvalStarved D-131 判据提取（纯读 atomics 可测）：open/lastEval/lastKick 快照 + 是否饥荒。
// 饥荒=有持仓 ∧ lastKickSent > lastEval+interval（事件送达 loop 超时未消化）。
func (e *Engine) exitEvalStarved() (open int64, lastEval, lastKick time.Time, starved bool) {
	open = e.openCount.Load()
	le := time.Unix(0, e.lastEvalAt.Load())
	lk := time.Unix(0, e.lastKickSent.Load())
	starved = open > 0 && !lk.IsZero() && lk.After(le.Add(e.fallbackInterval()))
	return open, le, lk, starved
}
