package exec

import (
	"context"
	"log"
)

// okxOrderFeeUSD D-391 S2：查单笔订单累计 fee → USD 正数成本（POST 直填路径未带 fee 时补查一次）。
// 查询失败/字段缺失/非 USD 系 feeCcy → 0（fail-open，leg 写 NULL 不写 0；log 注明）。
func (e *CEXExecutor) okxOrderFeeUSD(ctx context.Context, instID, ordID string) float64 {
	st, err := e.okxOrderStatus(ctx, instID, ordID)
	if err != nil {
		log.Printf("[exec] okx fee query ordId=%s: %v → fee_usd NULL", ordID, err)
		return 0
	}
	fee, ok := okxFeeToUSD(st.Fee, st.FeeCcy)
	if !ok {
		log.Printf("[exec] okx fee unavailable ordId=%s fee=%q feeCcy=%q → fee_usd NULL", ordID, st.Fee, st.FeeCcy)
		return 0
	}
	return fee
}
