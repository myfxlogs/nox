// Package spread 提供剩余可捕捉价差（sellBid−buyAsk）的归一 bps 计算（D-424）。
//
// 与入场口径同源：剩余价差 = 卖腿 bid − 买腿 ask。
// 只收编同公式同单位（bps 归一）调用点；原始价差阈值比较（未归一）不收。
package econ

// RemainingBps 返回 (sellBid−buyAsk)/ref*10000；spread≤0 或 ref≤0 时返回 0。
func RemainingBps(sellBid, buyAsk, ref float64) float64 {
	s := sellBid - buyAsk
	if s <= 0 || ref <= 0 {
		return 0
	}
	return s / ref * 10000
}
