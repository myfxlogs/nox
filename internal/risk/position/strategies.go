// Package position 持仓监控 + 策略类型分类（P3 单一真相）。
package position

import "arb/internal/review/verification"

// IsCarryType 判断持仓类型是否为息差型（长期持有，不适用收敛告警/收敛平仓）。
// 收敛：委托 verification.IsCarryType（SSOT），本包不再维护独立字符串列表。
// 收敛型 = CROSS_VENUE/MEANREVERT 及未知类型（默认按收敛处理）。
func IsCarryType(posType string) bool {
	return verification.IsCarryType(posType)
}
