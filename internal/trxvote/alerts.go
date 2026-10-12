// alerts.go —— TRX 投票改投/领奖告警渲染 + convertVotes 适配（自 scheduler.go 折分，D-120 行数门禁）。
package trxvote

import "fmt"

// convertVotes 将 map[string]int64（gotron-sdk Votes，TRX 单位）转为 []VoteEntry（链上 vote_count 已是 TRX 无需换算）。
func convertVotes(votes map[string]int64) []VoteEntry {
	out := make([]VoteEntry, 0, len(votes))
	for addr, v := range votes {
		out = append(out, VoteEntry{SRAddress: addr, Votes: float64(v)})
	}
	return out
}

// formatRebalanceAlert 格式化改投告警（设计 §8，D-293 S4 加 SR 变化明细）。
func formatRebalanceAlert(txHash string, d RebalanceDecision, currentVotes []VoteEntry) string {
	short := txHash
	if len(short) > 16 {
		short = short[:16]
	}
	return "[投票改投] tx=" + short +
		" yield " + formatFloat(d.CurrentYield) + "→" + formatFloat(d.TargetYield) +
		" TRX/yr (+" + formatFloat(d.Improvement) + "%)" +
		" SR " + topSRPrefix(currentVotes) + "→" + topSRPrefix(d.Target)
}

// topSRPrefix 取票数最高 SR 地址前 8 字符（无投票返回 "无"）。
func topSRPrefix(votes []VoteEntry) string {
	if len(votes) == 0 {
		return "无"
	}
	top := votes[0]
	for _, v := range votes[1:] {
		if v.Votes > top.Votes {
			top = v
		}
	}
	addr := top.SRAddress
	if len(addr) > 8 {
		addr = addr[:8]
	}
	return addr
}

// formatClaimAlert 格式化领奖告警（设计 §8）。
func formatClaimAlert(txHash string, amount int64) string {
	short := txHash
	if len(short) > 16 {
		short = short[:16]
	}
	return "[投票领奖] tx=" + short + " amount=" + formatFloat(float64(amount)) + " TRX"
}

// formatFloat 格式化浮点数（4 位小数）。
func formatFloat(f float64) string {
	return fmt.Sprintf("%.4f", f)
}
