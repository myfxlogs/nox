package autotrade

import (
	"fmt"

	"arb/internal/bus"
)

// legSpreadTooWide D-159 W4a R7：两腿 bid-ask spread 宽度门禁纯函数。
// maxBps<=0 → 不校验（返回 false）；任一腿 mid<=0 或 spread>maxBps → 返回 (true, reason)。
// reason 用于 log 跳过原因（调用方拼 canonical）。
func legSpreadTooWide(qLong, qShort bus.Quote, maxBps float64) (bool, string) {
	if maxBps <= 0 {
		return false, ""
	}
	longMid := qLong.Mid()
	shortMid := qShort.Mid()
	if longMid <= 0 || shortMid <= 0 {
		return true, fmt.Sprintf("mid<=0 (longMid=%.6f, shortMid=%.6f)", longMid, shortMid)
	}
	longSpreadBps := (qLong.Ask - qLong.Bid) / longMid * 10000
	shortSpreadBps := (qShort.Ask - qShort.Bid) / shortMid * 10000
	if longSpreadBps > maxBps || shortSpreadBps > maxBps {
		return true, fmt.Sprintf("leg spread too wide (long=%.2f bps, short=%.2f bps, max=%.1f)",
			longSpreadBps, shortSpreadBps, maxBps)
	}
	return false, ""
}

// entryBasisGateCheck D-169 P0 S3b：入场基差合理性门禁纯函数（可单测）。
// maxBps<=0 → 不校验（向后兼容）；basisAvailable=false（盲腿）→ fail-closed 拒绝；
// |basisBps| > maxBps → 拒绝（正=虚假价差，负=基差已偏离）。
func entryBasisGateCheck(basisBps float64, basisAvailable bool, maxBps float64) (bool, string) {
	if maxBps <= 0 {
		return false, ""
	}
	if !basisAvailable {
		return true, "blind leg"
	}
	absBps := basisBps
	if absBps < 0 {
		absBps = -absBps
	}
	if absBps > maxBps {
		return true, "divergence"
	}
	return false, ""
}

// entrySpreadMaxGateCheck D-169 P0 S3c：入场价差上限纯函数（可单测）。
// maxBps<=0 → 不校验（向后兼容）；spreadBps > maxBps → 拒绝（"太好的价差"=虚假价差）。
func entrySpreadMaxGateCheck(spreadBps, maxBps float64) (bool, string) {
	if maxBps <= 0 {
		return false, ""
	}
	if spreadBps > maxBps {
		return true, "too good"
	}
	return false, ""
}
