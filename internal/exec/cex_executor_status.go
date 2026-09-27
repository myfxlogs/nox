// cex_executor_status.go —— D-421：OKX 订单级状态查询/撤单/费折算（自 helpers 拆出消 >300 行）。
// 订单级查单（ordId/clOrdId）供成交确认轮询与 reconcile 消解器/收编前置守卫共用。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// okxOrderStatusResult GET /api/v5/trade/order 单条订单状态。
type okxOrderStatusResult struct {
	AccFillSz          string `json:"accFillSz"`
	AvgPx              string `json:"avgPx"`
	State              string `json:"state"`
	CancelSourceReason string `json:"cancelSourceReason"`
	Fee                string `json:"fee"`
	FeeCcy             string `json:"feeCcy"`
}

// okxOrderStatus 查询单笔订单成交（GET /api/v5/trade/order，签名四头 + simulated 头）。
// ordId 优先，空则 clOrdId 兜底（D-421 消解器按 clOrdId 查单凭据）。
// D-391 S2：+fee/feeCcy——实证（2026-09-22 pos498 ordId=3943917115077283840 实拍）：
// order 响应含累计 fee="-2.18095" feeCcy="USDT"（负号=付出）。
func (e *CEXExecutor) okxOrderStatus(ctx context.Context, instID, ordID, clOrdID string) (okxOrderStatusResult, error) {
	var out okxOrderStatusResult
	path := "/api/v5/trade/order?instId=" + url.QueryEscape(instID)
	if ordID != "" {
		path += "&ordId=" + url.QueryEscape(ordID)
	} else {
		path += "&clOrdId=" + url.QueryEscape(clOrdID)
	}
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	prehash := ts + "GET" + path
	sign := okxSign(e.apiSecret, prehash)
	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
	}
	body, err := e.get(ctx, e.baseURL+path, headers)
	if err != nil {
		return out, err
	}
	var resp struct {
		Code string                 `json:"code"`
		Data []okxOrderStatusResult `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return out, fmt.Errorf("okx order status json: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return out, fmt.Errorf("okx order status: code=%s", resp.Code)
	}
	return resp.Data[0], nil
}

// okxCancelOrder 撤销单笔订单（POST /api/v5/trade/cancel-order，D-421 窗尽双证技巧）：
// 调用方不判错误文案——cancel 成功/「已成交」报错均导向终查 accFillSz 定终态。
func (e *CEXExecutor) okxCancelOrder(ctx context.Context, instID, ordID string) error {
	path := "/api/v5/trade/cancel-order"
	bodyStr := fmt.Sprintf(`{"instId":%q,"ordId":%q}`, instID, ordID)
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	sign := okxSign(e.apiSecret, ts+"POST"+path+bodyStr)
	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
		"Content-Type":         {"application/json"},
	}
	_, err := e.post(ctx, e.baseURL+path, bodyStr, headers)
	return err
}

// okxFeeToUSD D-391 S2：OKX order/fills 响应 fee 字段 → USD 正数成本。
// venue 原始负号=付出（实证 fee="-2.18095"）→ 取绝对值；feeCcy 非 USD 稳定币 → ok=false
// （fail-open 回退 NULL，不猜换算——KB unspecified-err-handling-defaults-unsafe）。
func okxFeeToUSD(feeStr, feeCcy string) (float64, bool) {
	if feeStr == "" {
		return 0, false
	}
	fee, err := strconv.ParseFloat(feeStr, 64)
	if err != nil || fee == 0 {
		return 0, false
	}
	switch feeCcy {
	case "USD", "USDT", "USDC":
		return math.Abs(fee), true
	}
	return 0, false
}

// CancelOrder D-427 I2：rollback UNKNOWN+OrderID 孤儿出证——按 venue 分发撤单。
// 调用方不判错误文案；随后终查 QueryOrderStatus 定真实成交量。
func (e *CEXExecutor) CancelOrder(ctx context.Context, ref OrderRef) error {
	switch e.venue {
	case "okx":
		return e.okxCancelOrder(ctx, ref.Symbol, ref.OrderID)
	case "binance":
		return e.binanceCancelOrder(ctx, ref.Symbol, ref.OrderID)
	}
	return fmt.Errorf("cancel unsupported venue %s", e.venue)
}
