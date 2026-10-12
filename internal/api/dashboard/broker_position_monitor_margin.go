// broker_position_monitor_margin.go —— D-036：MT5 仓位级保证金规格估算（D-068 起为
// RequiredMargin RPC 失败/未接线时的 fail-closed 降级径，margin_is_estimate 标「估算」；
// G5（SymbolParams margin 字段）实测未实装，估径是唯一兜底，不删）。
// 估算式：SymGroup.InitialMargin>0 → InitialMargin×lots；否则 lots×ContractSize
// ×openPrice×init_margin_rate（fact 按裸 venue 键读）。规格/率任一不可得=空态不猜。
// 规格与 imr 在 prepareVenueView（reconcile 时点）随 vs.cs 同批拉取——帧路径零 RPC。
package dashboard

import (
	"context"

	"arb/internal/data/bus"
)

// symMarginSpec venue 原生符号保证金规格（SymbolParams 派生，reconcile 时点缓存）。
type symMarginSpec struct {
	initialMargin float64 // SymGroup.InitialMargin per-lot 定额（>0 直乘 lots）
	contractSize  float64 // SymbolInfo.ContractSize（率径名义分母）
}

// SetInitMarginRateFor D-036：账户级 init_margin_rate fact 读口注入
// （裸 venue 键；nil=率径估算不可用→仅 InitialMargin 定额径可产出）。
func (m *BrokerPositionMonitor) SetInitMarginRateFor(fn func(ctx context.Context, venue string) (rate float64, ok bool)) {
	m.initMarginRateFor = fn
}

// SetStaleTickFallback D-162 c：ticks 末帧兜底读口注入（MT5 休市/重启后展示价用；
// 仅展示径消费——Fresh 恒 stale，判定径不经此值）。
func (m *BrokerPositionMonitor) SetStaleTickFallback(fn func(ctx context.Context, venueKey, symbol string) (bus.Quote, bool)) {
	m.staleTickFor = fn
}

// estimateMargin 仓位级保证金估算（D-036 公式）：
// InitialMargin>0 → InitialMargin×lots；否则 lots×ContractSize×openPrice×InitMarginRate。
// 规格缺/率缺 → ok=false（诚实空态不猜）。
func estimateMargin(spec symMarginSpec, imr float64, imrOK bool, lots, openPrice float64) (float64, bool) {
	if spec.initialMargin > 0 {
		return spec.initialMargin * lots, true
	}
	if spec.contractSize > 0 && imrOK && imr > 0 && openPrice > 0 {
		return lots * spec.contractSize * openPrice * imr, true
	}
	return 0, false
}
