// D-236 变更9：LiveState 单结构快照（有流用流原则的统一载体）。
// 从 ws.go 拆出控行数 <300（LiveState 结构 + LiveState() 访问器 + EvalAsk 薄委托）。
package cmc

import "time"

// LiveState D-236 变更9：一次锁读的内存态快照（有流用流原则的统一载体）。
// position 域（positionsMu 一锁）：Ask/Mid/Qty/Lev/PnL + PosTs
// summary 域（summaryMu 一锁）：Cash/Equity/Margin + SumTs
// 替代 EvalAsk/EvalPosition/EvalSummary 三访问器堆积（自审 B 项违规修正）。
type LiveState struct {
	Ask, Mid, Qty, Lev, PnL float64 // position 域（X-AFQUL 内存态）
	PosTs                   time.Time
	Cash, Equity, Margin    float64 // summary 域（type6 内存态）
	SumTs                   time.Time
}

// LiveState D-236 变更9：一次锁读的内存态快照（有流用流原则的统一载体）。
// position 域从 positionsMu 读 X-AFQUL 内存态（Ask/Mid/Qty/Lev/PnL），PosTs 从 priceMu 读 evalAskTs；
// summary 域从 summaryMu 读 type6 内存态（Cash/Equity/Margin），SumTs 从 summaryMu 读 lastSummaryTs。
// 替代 EvalAsk/EvalPosition/EvalSummary 三访问器堆积（自审 B 项违规修正）。
func (w *WsClient) LiveState() LiveState {
	w.positionsMu.Lock()
	var ask, mid, qty, pnl float64
	var lev float64
	for i := range w.positions {
		if w.positions[i].InstrumentID == "X-AFQUL" {
			ask = parseFloat(w.positions[i].EvaluationAskPrice)
			bid := parseFloat(w.positions[i].EvaluationBidPrice)
			if ask > 0 && bid > 0 {
				mid = (ask + bid) / 2
			} else {
				mid = ask // 回退 ask（bid=0 时）
			}
			qty = parseFloat(w.positions[i].Quantity)
			pnl = parseFloat(w.positions[i].UnrealisedPnLInPrimaryCurrency)
			marginPct := parseFloat(w.positions[i].StandardMarginPercentage)
			if marginPct > 0 {
				lev = 1.0 / marginPct
			}
			break
		}
	}
	w.positionsMu.Unlock()
	w.priceMu.Lock()
	posTs := w.evalAskTs
	w.priceMu.Unlock()
	w.summaryMu.Lock()
	cash := parseFloat(w.lastSummary.TotalCashBalance)
	equity := parseFloat(w.lastSummary.TotalAccountValue)
	margin := parseFloat(w.lastSummary.TotalEquityUsedInMargin)
	sumTs := w.lastSummaryTs
	w.summaryMu.Unlock()
	return LiveState{
		Ask: ask, Mid: mid, Qty: qty, Lev: lev, PnL: pnl, PosTs: posTs,
		Cash: cash, Equity: equity, Margin: margin, SumTs: sumTs,
	}
}

// EvalAsk D-234 R-V S1 → D-236 变更9 退役为薄委托（LiveState().Ask）。
// 保留签名兼容既有调用方；新鲜度 >30s 由调用方回退 DB。
func (w *WsClient) EvalAsk() (float64, time.Time) {
	ls := w.LiveState()
	return ls.Ask, ls.PosTs
}
