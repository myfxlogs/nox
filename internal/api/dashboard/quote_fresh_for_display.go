// d155_pulse.go —— D-155 方案A：展示侧报价新鲜度分档（活性脉冲续命）。
// 薄品种（OKX tickers 变更驱动推送）簿面静止时帧稀疏→quote.Time 超 freshTTL 但
// 报价值仍是 venue 当前最优簿，缺的只是活性证据。mark-price/index-tickers 脉冲
// 经 QuoteBus.Touch 打点补足活性证据；分档：
//
//	quote.Time ≤ freshTTL                      → fresh（原语义）
//	freshTTL < age ≤ pulseHardCap 且 pulse 鲜   → fresh-extended（簿面未变+活性有证）
//	其余                                       → stale/est（原语义）
//
// 禁区遵守：eval/econSnapshot/disaster/harvest/退出闸全部零动——quote.Time 不被脉冲
// 污染，fail-closed 语义原样；本分档只作用展示层。
package dashboard

import (
	"time"

	"nox/internal/data/bus"
)

// quotePulseHardCap 脉冲续命上限（票面③钦定 ≤30min——超此连脉冲也救不回，防
// 单 instId tickers 独死被无限掩）。仅展示层。
const quotePulseHardCap = 30 * time.Minute

// quoteFreshForDisplay 报价新鲜度分档判定（D-155 方案A）：见文件头三档。
// 脉冲鲜度窗=freshTTL（脉冲帧实测亚秒级密推，120s 窗极宽）。
func quoteFreshForDisplay(qbus *bus.QuoteBus, q bus.Quote, freshTTL time.Duration, now time.Time) bool {
	if now.Sub(q.Time) <= freshTTL {
		return true
	}
	if qbus == nil || now.Sub(q.Time) > quotePulseHardCap {
		return false
	}
	if t, ok := qbus.PulseAt(q.Symbol, q.VenueKey()); ok && now.Sub(t) <= freshTTL {
		return true // fresh-extended：簿面未变 + 活性有证
	}
	return false
}
