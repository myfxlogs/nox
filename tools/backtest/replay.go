// replay.go —— 参数-PnL 回放矩阵（D-625 P3）：簿行序上模拟进出场。
// 模型口径（报告显式标注）：entry_net_bps≥阈开仓（判定链自身净入口径），
// exit_spread_bps≤阈 或持满 hold 平仓；单笔 PnL=entry_net−exit_spread（bps，
// 簿行冻结值——不重算报价，禁口径漂移）。窗未过准入闸禁入本层（上游闸守）。
package main

import "time"

// Params 回放参数组（矩阵格点）。
type Params struct {
	EntryBps  float64
	ExitBps   float64
	HoldHours float64
}

// Trade 单笔回放交易。
type Trade struct {
	EntryTS, ExitTS time.Time
	EntryNetBps     float64
	ExitSpreadBps   float64
	PnLBps          float64 // entry_net − exit_spread
}

// MatrixRow 参数格点汇总行。
type MatrixRow struct {
	Params
	Trades   int
	Wins     int
	TotalBps float64
	AvgHoldH float64
}

// Replay 单参数组回放（rows 按 ts 升序；跨参数复用同一行序=同窗同证据）。
func Replay(rows []BookRow, p Params) []Trade {
	var trades []Trade
	holding := false
	var entry BookRow
	for _, r := range rows {
		if !holding {
			if r.EntryNetBps >= p.EntryBps {
				holding, entry = true, r
			}
			continue
		}
		holdElapsed := r.TS.Sub(entry.TS).Hours() >= p.HoldHours
		if r.ExitSpreadBps <= p.ExitBps || holdElapsed {
			trades = append(trades, Trade{
				EntryTS: entry.TS, ExitTS: r.TS,
				EntryNetBps: entry.EntryNetBps, ExitSpreadBps: r.ExitSpreadBps,
				PnLBps: entry.EntryNetBps - r.ExitSpreadBps,
			})
			holding = false
		}
	}
	// 窗尾未平仓=如实不记账（禁拿窗尾价伪造平仓——半笔交易非证据）。
	return trades
}

// Matrix 参数格点×窗回放 → 汇总矩阵（每格独立回放，行序共享）。
func Matrix(rows []BookRow, grid []Params) []MatrixRow {
	out := make([]MatrixRow, 0, len(grid))
	for _, p := range grid {
		trades := Replay(rows, p)
		m := MatrixRow{Params: p, Trades: len(trades)}
		var holdH float64
		for _, t := range trades {
			if t.PnLBps > 0 {
				m.Wins++
			}
			m.TotalBps += t.PnLBps
			holdH += t.ExitTS.Sub(t.EntryTS).Hours()
		}
		if m.Trades > 0 {
			m.AvgHoldH = holdH / float64(m.Trades)
		}
		out = append(out, m)
	}
	return out
}
