package exec

import "fmt"

// generateClientOrderID D-195-B §4.3：确定性 client_order_id 生成。
// 输入 request_id + position_id + leg_no + attempt_no，输出 venue-safe 编码。
// 格式：{requestID}-{posID}-{legNo}-{attemptNo}，截断到 32 字符（最短 venue 限制）。
// venue 不支持 client_order_id 时调用方传空字符串（不伪造）。
func generateClientOrderID(requestID string, posID int64, legNo, attemptNo int16) string {
	if requestID == "" {
		return "" // 无 request_id 时不生成（向后兼容）
	}
	raw := fmt.Sprintf("%s-%d-%d-%d", requestID, posID, legNo, attemptNo)
	if len(raw) > 32 {
		// 截断 request_id 部分，保留后缀确定性
		suffix := fmt.Sprintf("-%d-%d-%d", posID, legNo, attemptNo)
		raw = raw[:32-len(suffix)] + suffix
	}
	return raw
}
