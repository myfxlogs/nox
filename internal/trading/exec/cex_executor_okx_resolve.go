// cex_executor_okx_resolve.go —— 下单超时 clOrdId 定态消解（真钱加固件）。
// POST 传输层败后禁盲重发（重复单真钱险）——先 GET 订单定真态：
// placed→ack 成功（Fill 回填）；absent→可重试；查询亦败=歧态势→critical 告警。
package exec

import (
	"context"
	"fmt"
	"time"
)

// okxOrderResolveByClOrdId 下单超时定态：clOrdId 查单→placed 回填 Fill，
// absent（venue code≠0/无 data）→原错透传可重试，查询败→歧态势告警不补单。
func (e *CEXExecutor) okxOrderResolveByClOrdID(ctx context.Context, o Order, instID, side string, ctVal float64, sendErr error) (Fill, error) {
	st, qerr := e.okxOrderStatus(ctx, instID, "", o.ClientOrderID)
	if qerr != nil {
		e.alertCritical(o, fmt.Sprintf("okx 下单超时且状态查询败=歧态势（持仓#%d %s %s clOrdId=%s）勿补单：send=%v query=%v",
			o.PosID, o.Symbol, side, o.ClientOrderID, sendErr, qerr))
		return Fill{}, fmt.Errorf("okx order 歧态势（查询败禁重试）: send=%w query=%v", sendErr, qerr)
	}
	qty, _ := f64z(st.AccFillSz)
	avgPx, _ := f64z(st.AvgPx)
	if e.venueType == "perp" {
		qty *= ctVal
	}
	if st.State == "filled" && qty > 0 && avgPx > 0 {
		feeUSD := e.okxOrderFeeUSD(ctx, instID, st.OrdID)
		return Fill{OrderID: st.OrdID, Price: avgPx, Qty: qty,
			Commission: feeUSD, FeeUSD: feeUSD, FillKind: "taker", Ts: time.Now()}, nil
	}
	// placed/live/partially_filled=已落单——走既有 confirm 轮询件定终态。
	if st.State == "live" || st.State == "partially_filled" {
		return e.okxConfirmFill(ctx, o, instID, st.OrdID, ctVal)
	}
	// absent/canceled/无单——原始 send 败透传（调用面可安全重试幂等键不变）。
	return Fill{}, fmt.Errorf("okx order 未落单（查单 absent 可重试）: %w", sendErr)
}
