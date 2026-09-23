package exec

import (
	"fmt"
	"strings"
)

// generateClientOrderID D-195-B §4.3：确定性 client_order_id 生成。
// 输入 request_id + position_id + leg_no + attempt_no，输出 venue-safe 编码。
// 格式：{requestID}-{posID}-{legNo}-{attemptNo}，截断到 32 字符（最短 venue 限制）。
// venue 不支持 client_order_id 时调用方传空字符串（不伪造）。
func generateClientOrderID(requestID string, posID int64, legNo, attemptNo int16) string {
	if requestID == "" {
		return "" // 无 request_id 时不生成（向后兼容）
	}
	suffix := fmt.Sprintf("-%d-%d-%d", posID, legNo, attemptNo)
	// D-413：venue-safe 消毒——OKX clOrdId 只收 [A-Za-z0-9_-]。autotrade requestID 内嵌
	// opp.ID（"XASSET_CARRY:XAUUSD:3001157:mt5:okx:perp"）含冒号（非法字符）且截断落点
	// 可产双横线/边缘横线（499/500 实证 sCode=51000 clOrdId error → LEGGED 裸腿）。
	head := sanitizeClOrdID(requestID)
	if maxHead := 32 - len(suffix); len(head) > maxHead {
		head = head[:maxHead]
	}
	head = strings.TrimRight(head, "-_") // 截断尾部不能留分隔符（拼接后缀出 "--"）
	if head == "" {
		head = "x"
	}
	return head + suffix
}

// sanitizeClOrdID 把 requestID 收敛到 venue-safe 字符集：字母/数字/下划线保留，
// 其余一律折叠为单个 '-'（含已有 '-'），边缘横线剔除。
func sanitizeClOrdID(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastDash := false
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if lastDash {
			continue // 连续非法字符折叠为一个 '-'
		}
		lastDash = true
		b.WriteByte('-')
	}
	return strings.Trim(b.String(), "-")
}
