package autotrade

import (
	"fmt"
	"time"
)

// perpFundingExitDecision D-133 T1：PERP_FUNDING 退出判定纯函数（持有下限 + 连续去抖）。
//
// 输入：factOK（funding facts 是否齐备）、held（now−opened_at）、netFunding（年化百分点）、
// exitPct（退出阈值，默认 0）、minHold（持有下限，默认 8h）、debounceN（连续去抖次数，默认 3）、
// curCount（当前连续 breach 计数）。
//
// 输出：(shouldClose, newCount, reason)。
//
// 语义：
//   - factOK=false（缺数据）→ 不平仓不计数（fail-closed，D-105 语义不变；newCount=curCount 原样回传）。
//   - floor 未满（held < minHold）→ 不平仓 + 计数清零（newCount=0）——至少活过一个结算窗。
//   - floor 满 + breach（netFunding ≤ exitPct）→ 计数+1，newCount ≥ debounceN → 平仓。
//   - floor 满 + 不 breach → 计数清零（newCount=0）——任一次读数回正即重置。
//   - debounceN ≤ 1 → 单次 breach 即平（近似旧行为，向后兼容）。
//   - minHold ≤ 0 → 不限持有时长（floor 永远满足，仅 debounce 起作用）。
//
// 纯函数抽出供单测（先红后绿）：①floor 未满+breach→不平且清零 ②floor 满+单次→不平（计数 1）
// ③连续 N 次→平（reason 含 held/count）④读数回正→清零 ⑤缺 fact→不平不计数。
func perpFundingExitDecision(factOK bool, held time.Duration, netFunding, exitPct float64, minHold time.Duration, debounceN, curCount int) (shouldClose bool, newCount int, reason string) {
	// ⑤ 缺数据：fail-closed 不决策，计数原样保留（不累计也不清零）。
	if !factOK {
		return false, curCount, ""
	}
	// ① floor 未满：不平仓 + 计数清零（持仓未活过一个结算窗，收益源先参与再谈退出）。
	if minHold > 0 && held < minHold {
		return false, 0, ""
	}
	// breach 判定：netFunding ≤ exitPct（默认 0）——与旧分支阈值语义一致。
	if netFunding <= exitPct {
		newCount = curCount + 1
		// ② count < N → 不平；③ count ≥ N → 平（debounceN ≤ 1 → 单次即平）。
		if debounceN <= 1 || newCount >= debounceN {
			heldH := held.Hours()
			return true, newCount, fmt.Sprintf("held %.1fh, net funding %.2f%% <= %.1f%% for %d checks", heldH, netFunding, exitPct, newCount)
		}
		return false, newCount, ""
	}
	// ④ 读数回正：计数清零（任一次读数回正即重置连续 breach 序列）。
	return false, 0, ""
}
