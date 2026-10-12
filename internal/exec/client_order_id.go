package exec

import (
	"fmt"
	"strings"
)

// generateClientOrderID D-195-B §4.3：确定性 client_order_id 生成。
// 输入 request_id + position_id + leg_no + attempt_no，输出 venue-safe 编码。
// 格式：{requestID 消毒头}p{posID}l{legNo}a{attemptNo}，截断到 32 字符（最短 venue 限制）。
// venue 不支持 client_order_id 时调用方传空字符串（不伪造）。
func generateClientOrderID(requestID string, posID int64, legNo, attemptNo int16) string {
	if requestID == "" {
		return "" // 无 request_id 时不生成（向后兼容）
	}
	suffix := fmt.Sprintf("p%dl%da%d", posID, legNo, attemptNo)
	// D-413：venue-safe 消毒——OKX clOrdId 只收纯字母数字 [a-zA-Z0-9]（官方："case-sensitive
	// alphanumerics, all numbers, or all letters"，连 '-'/'_' 都非法）。autotrade requestID
	// 内嵌 opp.ID（"XASSET_CARRY:XAUUSD:3001157:mt5:okx:perp"）含冒号——499/500/501 三连
	// sCode=51000 clOrdId error → LEGGED 裸腿实证：冒号版拒、双横线版拒、单横线版仍拒。
	head := sanitizeClOrdID(requestID)
	if maxHead := 32 - len(suffix); len(head) > maxHead {
		head = head[:maxHead]
	}
	if head == "" {
		head = "x"
	}
	return head + suffix
}

// sanitizeClOrdID 把 requestID 收敛到最严 venue 字符集（OKX clOrdId）：纯 [a-zA-Z0-9]，
// 其余字符一律剥除（不折叠——任何分隔符对 OKX 均非法）。
func sanitizeClOrdID(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
