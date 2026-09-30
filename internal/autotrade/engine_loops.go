package autotrade

import (
	"context"
	"time"
)

// positionFallbackInterval D-107 P4：低频兜底扫描间隔旧值（10min）。
// D-378a S3 起默认 60s 配置化（ARB_POSITION_EVAL_SEC→cfg.PositionFallbackInterval）；
// 本常量保留为 ≤0 防呆回退值。包级 var 测试可覆写（marginReconnectBackoff 先例）。
var positionFallbackInterval = 10 * time.Minute

// fallbackInterval D-378a S3：评估节拍——修复评估饥荒（无订单事件时退出评估最多饿 10min，
// 「T 分钟持续」类判据无定义域）。scanAndClose 全仓扫描（≤50 仓×内存 quote 读）60s 成本可忽略；
// 顺带治愈 SWAPARB 等其他型同款饥荒。事件驱动路径（orderTopic→kick→30s 节流）逐字节不动。
func (e *Engine) fallbackInterval() time.Duration {
	if e.cfg.PositionFallbackInterval > 0 {
		return e.cfg.PositionFallbackInterval
	}
	return positionFallbackInterval
}

// Kick D-107 P4：orderTopic 事件触发重扫（非阻塞 send 到 buffered(1) chan——合并语义同 P2 监控）。
func (e *Engine) Kick() {
	select {
	case e.kickCh <- struct{}{}:
	default: // 已有待处理 kick，合并丢弃
	}
}

// scanIfDue D-107 P4：kick 触发节流扫描——距上次扫描 < ScanThrottle → 丢弃（兜底 10min 保证有界延迟）；
// 否则执行 scanAndClose + stateMachineTick + 更新 lastScan。
// lastScan 无锁：仅 positionLoop goroutine 读写（quoteReconIdx 单协程先例）。
func (e *Engine) scanIfDue(ctx context.Context) {
	if time.Since(e.lastScan) < e.cfg.ScanThrottle {
		return // 节流窗口内丢弃
	}
	e.scanAndClose(ctx)
	e.stateMachineTick(ctx)
	e.lastScan = time.Now()
}

// positionLoop D-107 P4：启动即扫 → select 三支（kickCh→scanIfDue / fallback→直扫 / ctx.Done）。
// 事件驱动优先（orderTopic → Kick → scanIfDue 30s 节流），fallback 10min 兜底（防事件丢失窗口）。
func (e *Engine) positionLoop(ctx context.Context) {
	// 启动即扫（首个扫描立即可用 + 设 lastScan 节流基准）。
	e.scanAndClose(ctx)
	e.stateMachineTick(ctx)
	e.lastScan = time.Now()

	fallback := time.NewTicker(e.fallbackInterval()) // D-378a S3：节拍配置化（D-562 注记：事件驱动 kick 主路+本 ticker=评估饥荒兜底节拍，退出判定消费 qbus 实时报价非轮询快照——判定证据实时合规）
	defer fallback.Stop()
	csMissTick := time.NewTicker(csMissAlertInterval) // D-140 S2：CS-miss 聚合告警
	defer csMissTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.kickCh:
			e.scanIfDue(ctx) // D-107 P4：事件触发节流扫描
		case <-fallback.C:
			// D-107 P4：兜底直扫（不受节流限制——10min 有界延迟保证）。
			e.scanAndClose(ctx)
			e.stateMachineTick(ctx)
			e.lastScan = time.Now()
		case <-csMissTick.C:
			e.flushCSMissAlerts() // D-140 S2
		}
	}
}

// quoteKickDispatch D-469c S1/S2：qbus 全订 → 每帧非阻塞 send e.kickCh（positionLoop
// scanIfDue 30s 节流合流）；canonical ∈ MeanRevertThresh 时另 send mrKick。
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
			e.Kick() // S2：positionLoop quote-kick（节流在 scanIfDue 侧）
			if _, tracked := e.cfg.MeanRevertThresh[q.Symbol]; tracked {
				e.KickMeanRevert() // S1：meanRevert 入场扫描 kick
			}
		}
	}
}
