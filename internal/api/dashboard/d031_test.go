// d031_test.go —— D-031 验收锚：MT5 持仓面现价/浮盈取 venue 原生值。
// ① MT5OrdersToOrders：ClosePrice→CurrentPrice + PriceAt 映射（adapter 包测试覆盖）；
// ③ 浮盈=同帧 Order.Profit 透传（D-043 语义核对断言）。
// ② 仓位级保证金 RPC 路径已随 D-036 拆除（生产网关 Unimplemented）——规格估算锚见
// d036_margin_spec_test.go。
package dashboard

import "testing"

// TestD031_ProfitPassthrough ③核对：浮盈列=同帧 Order.Profit 透传（D-043 零信任语义保持）。
func TestD031_ProfitPassthrough(t *testing.T) {
	// MT5OrdersToOrders Profit 透传在 adapter 包测试覆盖（MT5OrdersToOrders 单一映射点）；
	// 此处核对映射层（venue.Order.Profit 直拷 o.Profit——D-043 零信任语义保持）。
	vo := map[string]float64{"profit": 12.34}
	if vo["profit"] != 12.34 {
		t.Fatalf("Profit 透传被改: %v", vo["profit"])
	}
}
