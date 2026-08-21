// Package position 持仓监控 + 策略类型分类（P3 单一真相）。
package position

// IsCarryType 判断持仓类型是否为息差型（长期持有，不适用收敛告警/收敛平仓）。
// SWAPARB/CARRY/FUNDING_HEDGE/PERP_FUNDING — 与 autotrade.Engine.checkExitCondition 的息差型分派同源（P3 单一真相）。
// 收敛型 = CROSS_VENUE/MEANREVERT 及未知类型（默认按收敛处理）。
// 两处引用（position/monitor.go 跳过收敛告警 + autotrade/engine.go 退出条件分派），勿复制字符串列表两份。
func IsCarryType(posType string) bool {
	switch posType {
	case "SWAPARB", "CARRY", "FUNDING_HEDGE", "PERP_FUNDING":
		return true
	}
	return false
}
