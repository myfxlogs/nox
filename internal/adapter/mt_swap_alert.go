// mt_swap_alert.go —— swap 轮次告警/观测：每轮汇总、整轮零产出 critical 判定、未覆盖
// SwapType 计数汇总（自 mt_swap_helpers.go 迁出，D-086 R1，纯移动零语义变更）。
package adapter

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// swapPollSummary 每轮每 venue 一行汇总（D-060 F1）：skip 路径零日志=探测面隐形收缩（P4）。
func swapPollSummary(src, venue string, emit, emit0 int, skips []string) {
	if len(skips) == 0 {
		log.Printf("[%s:%s] swap poll: emit=%d emit0=%d skip=0", src, venue, emit, emit0)
		return
	}
	log.Printf("[%s:%s] swap poll: emit=%d emit0=%d skip=%d [%s]", src, venue, emit, emit0, len(skips), strings.Join(skips, ", "))
}

// swapAttachWarmup D-086：attach ready 后网关暖窗——backfill 期 swap poll 全
// DeadlineExceeded 属瞬时（10-06 01:15 实拍：71 符号 3s/call 全超时，01:17 即恢复产出）。
// 窗内整轮零产出只 log：零告警、不计数、不重置（D-085「attach 期不判」同哲学）。
const swapAttachWarmup = 5 * time.Minute

// swapZeroOutputAlert 整轮零产出（emit+emit0==0 且 skip/err>0）判定节奏（AlertSink 已
// 注入链 main→brokerMgr→ad；nil=no-op）。PXBT 双账户静默零 facts 实证（2026-09-30）：
// skip=81 但只有日志行无人看——禁静默升级为告警。D-086：有产出轮清零计数；attach 暖窗内
// 只 log（不计数不重置）；窗外连续 ≥2 轮才 critical（单轮瞬时超时降 log），6h 节流不变。
func (a *MT5Adapter) swapZeroOutputAlert(emit, emit0 int, skips, errs []string) {
	if emit+emit0 > 0 {
		a.mu.Lock()
		a.swapZeroRounds = 0
		a.mu.Unlock()
		return
	}
	if len(skips)+len(errs) == 0 || a.AlertSink == nil {
		return
	}
	a.mu.Lock()
	if readyAge := time.Since(a.readyAt); readyAge < swapAttachWarmup {
		a.mu.Unlock()
		log.Printf("[mt5:%s] swap 零产出（attach 暖窗 readyAge=%s）不告警：skip=%d err=%d",
			a.venue, readyAge.Round(time.Second), len(skips), len(errs))
		return
	}
	a.swapZeroRounds++
	if a.swapZeroRounds < 2 {
		a.mu.Unlock()
		log.Printf("[mt5:%s] swap 零产出连续 1 轮（瞬时超时观察中）：skip=%d err=%d",
			a.venue, len(skips), len(errs))
		return
	}
	if time.Since(a.swapZeroAlertAt) < swapZeroAlertInterval {
		a.mu.Unlock()
		return
	}
	a.swapZeroAlertAt = time.Now()
	a.mu.Unlock()
	reasons := append(append([]string{}, skips...), errs...)
	if len(reasons) > 5 {
		reasons = append(reasons[:5], fmt.Sprintf("…+%d", len(reasons)-5))
	}
	a.AlertSink("critical", fmt.Sprintf("[mt5:%s] swap 采集整轮零产出：skip=%d err=%d（%s）",
		a.venue, len(skips), len(errs), strings.Join(reasons, "; ")))
}

// swapUncoveredSummary WP1 未覆盖 SwapType 计数汇总（禁静默）：
// 有未覆盖类型时逐类型打印 `[swap] uncovered SwapType=%d count=%d`（对拍 aggregator-skip 计数纪律）。
// 无未覆盖类型时零日志（探测面全覆盖=无事可报）。
func swapUncoveredSummary(src, venue string, uncovered map[int32]int) {
	if len(uncovered) == 0 {
		return
	}
	// 排序输出（稳定日志序）
	keys := make([]int32, 0, len(uncovered))
	for k := range uncovered {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	for _, k := range keys {
		log.Printf("[%s:%s] [swap] uncovered SwapType=%d count=%d", src, venue, k, uncovered[k])
	}
}
