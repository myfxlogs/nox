// broker_position_monitor_margin.go —— D-036：MT5 仓位级保证金=venue 规格估算
// （D-031 ② 的 RequiredMargin RPC 路径拆除——生产网关 Unimplemented 无原生字段）。
// 估算式：SymGroup.InitialMargin>0 → InitialMargin×lots；否则 lots×ContractSize
// ×openPrice×init_margin_rate（fact 按裸 venue 键读）。规格/率任一不可得=空态不猜。
// 规格与 imr 在 prepareVenueView（reconcile 时点）随 vs.cs 同批拉取——帧路径零 RPC。
package dashboard

import "context"

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
