// cex_executor_maker.go —— D-018 阶段a：post-only 限价发单（三所）。
// 等待/撤单终查见 cex_executor_maker_wait.go（拆分：本文件=发单，≤300 行契约）。
// maker 只挂 CEX perp 腿（spot/MT 不适用——MT 无 post-only 语义，spot 腿恒 taker）。
// 只挂不追：post-only 被拒（会吃单）=终态错误，不重试；撤单终查双证——撤后必须以查单
// 终态回填成交部分（cancel 响应 executedQty 不足信，D-421 同源教训）。
// 成交方式钉死：post-only 成交必为 maker（只挂单会吃单即拒/撤），Fill.FillKind="maker"。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// roundToTick 限价 tick 取整（四舍五入到最近步长；tick<=0 = fail-closed 不猜步长）。
func roundToTick(px, tick float64) (float64, error) {
	if tick <= 0 {
		return 0, fmt.Errorf("price tick unavailable (fail-closed, 不挂未取整限价单)")
	}
	return math.Round(px/tick) * tick, nil
}

// PlacePostOnly 挂 post-only 限价单（perp 专用）：返回 venue 订单 ID（订单已受理/在挂）。
// 挂单即拒（会吃单：okx 51006/51007、binance -5022、gate poc 即撤）=错误返回（不 chase，
// 上层记 attempt 终态）；其余受理成功 → 订单在 book 上等待成交，调用方 AwaitPostOnlyFill/
// PostOnlyCancel 跟进。
func (e *CEXExecutor) PlacePostOnly(ctx context.Context, o Order, limitPrice float64) (string, error) {
	if e.venueType != "perp" {
		return "", fmt.Errorf("post-only: venueType %q 不适用（maker 腿恒 perp）", e.venueType)
	}
	if limitPrice <= 0 {
		return "", fmt.Errorf("post-only: limitPrice=%v 非法 fail-closed", limitPrice)
	}
	switch e.venue {
	case "okx":
		return e.okxPlacePostOnly(ctx, o, limitPrice)
	case "binance":
		return e.binancePlacePostOnly(ctx, o, limitPrice)
	case "gate":
		return e.gatePlacePostOnly(ctx, o, limitPrice)
	default:
		return "", fmt.Errorf("post-only: unsupported venue %q", e.venue)
	}
}

// --- okx ---

// okxPlacePostOnly POST /api/v5/trade/order ordType=post_only（perp cross + posSide 派生，
// 同 placeOKX perp 分支契约；sz=张数换算/lotSz floor 复用同源规格缓存）。
// 51006/51007=post-only 价格会立即吃单被拒——不 chase，终态错误（上层记 attempt）。
func (e *CEXExecutor) okxPlacePostOnly(ctx context.Context, o Order, px float64) (string, error) {
	side := "buy"
	if o.Side == "SELL" {
		side = "sell"
	}
	instID := o.Symbol
	if !strings.HasSuffix(instID, "-SWAP") {
		instID = instID + "-SWAP"
	}
	if err := e.okxPosModeCheck(ctx); err != nil {
		return "", err
	}
	if err := e.okxStateCheck(ctx, instID); err != nil {
		return "", err
	}
	ctVal, lotSz, _, err := e.okxContractVal(ctx, instID)
	if err != nil {
		return "", fmt.Errorf("okx contract spec: %w", err)
	}
	szContracts := math.Floor(o.Qty/ctVal/lotSz) * lotSz
	if szContracts < lotSz {
		return "", fmt.Errorf("okx post-only qty %.6f/ctVal %.6f=%.6f 张 < lotSz %.6f fail-closed", o.Qty, ctVal, o.Qty/ctVal, lotSz)
	}
	tick, err := OkxSwapTickSize(ctx, e.client, e.baseURL, instID)
	if err != nil {
		return "", fmt.Errorf("okx tickSz: %w", err)
	}
	pxR, err := roundToTick(px, tick)
	if err != nil {
		return "", fmt.Errorf("okx post-only px %.6f: %w", px, err)
	}
	posSide := o.PositionSide
	if posSide == "" {
		posSide = "long"
		if side == "sell" {
			posSide = "short"
		}
	}
	clOrdID := ""
	if o.ClientOrderID != "" {
		clOrdID = fmt.Sprintf(`,"clOrdId":%q`, o.ClientOrderID)
	}
	bodyStr := fmt.Sprintf(`{"instId":%q,"tdMode":"cross","side":%q,"ordType":"post_only","sz":%q,"px":%q,"posSide":%q,"reduceOnly":false%s}`,
		instID, side, strconv.FormatFloat(szContracts, 'f', -1, 64), strconv.FormatFloat(pxR, 'f', -1, 64), posSide, clOrdID)
	path := "/api/v5/trade/order"
	ts := e.okxNowISO()
	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {okxSign(e.apiSecret, ts+"POST"+path+bodyStr)},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
		"Content-Type":         {"application/json"},
		"expTime":              {strconv.FormatInt(time.Now().Add(15*time.Second).UnixMilli(), 10)},
	}
	body, err := e.post(ctx, e.baseURL+path, bodyStr, headers)
	if err != nil {
		return "", fmt.Errorf("okx post-only order: %w", err)
	}
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			OrdID string `json:"ordId"`
			SCode string `json:"sCode"`
			SMsg  string `json:"sMsg"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("okx post-only json: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 || (resp.Data[0].SCode != "" && resp.Data[0].SCode != "0") {
		detail := resp.Msg
		if len(resp.Data) > 0 && resp.Data[0].SMsg != "" {
			detail = fmt.Sprintf("%s (sCode=%s sMsg=%s)", resp.Msg, resp.Data[0].SCode, resp.Data[0].SMsg)
		}
		return "", fmt.Errorf("okx post-only 拒单: %s", detail)
	}
	return resp.Data[0].OrdID, nil
}

// --- binance ---

// binancePlacePostOnly POST /v1/order type=LIMIT timeInForce=GTX（Good Till Crossing=post-only，
// 会吃单即 -5022 拒）。qty 走 binanceQtyGuard LOT_SIZE floor（同 placeBinance 闸面缓存）。
func (e *CEXExecutor) binancePlacePostOnly(ctx context.Context, o Order, px float64) (string, error) {
	if o.PositionSide != "" {
		return "", fmt.Errorf("binance post-only：开仓腿 PositionSide 须空（平仓路径不适用 maker）")
	}
	qty, err := e.binanceQtyGuard(ctx, o.Symbol, o.Qty, 0)
	if err != nil {
		return "", err
	}
	f, ok := binanceFiltersLoad(o.Symbol)
	if !ok {
		if f, err = e.binanceFiltersFetch(ctx, o.Symbol); err != nil {
			return "", fmt.Errorf("binance filters: %w", err)
		}
	}
	if f.TickSize <= 0 {
		return "", fmt.Errorf("binance PRICE_FILTER.tickSize 未采集 fail-closed")
	}
	pxR, err := roundToTick(px, f.TickSize)
	if err != nil {
		return "", fmt.Errorf("binance post-only px %.6f: %w", px, err)
	}
	params := url.Values{}
	params.Set("symbol", o.Symbol)
	params.Set("side", strings.ToUpper(o.Side))
	params.Set("type", "LIMIT")
	params.Set("timeInForce", "GTX")
	params.Set("quantity", strconv.FormatFloat(qty, 'f', -1, 64))
	params.Set("price", strconv.FormatFloat(pxR, 'f', -1, 64))
	if o.ClientOrderID != "" {
		params.Set("newClientOrderId", o.ClientOrderID)
	}
	res, err := e.signedBinanceRequest(ctx, "POST", "/v1/order", params)
	if err != nil {
		// -5022 = post-only 会吃单被拒——不 chase，终态错误。
		return "", fmt.Errorf("binance post-only 拒单: %w", err)
	}
	id, _ := res["orderId"].(float64)
	if id == 0 {
		return "", fmt.Errorf("binance post-only: orderId 缺失 resp=%v", res)
	}
	return strconv.FormatInt(int64(id), 10), nil
}

// --- gate ---

// gatePlacePostOnly POST /futures/usdt/orders tif=poc（price-or-cancelled=post-only：
// 会吃单即下单即撤，status=finished/finish_as=cancelled——按拒单终态处理不 chase）。
func (e *CEXExecutor) gatePlacePostOnly(ctx context.Context, o Order, px float64) (string, error) {
	sizeF, spec, err := e.gateSpecCheck(ctx, o.Symbol, o.Qty)
	if err != nil {
		return "", fmt.Errorf("gate order size: %w", err)
	}
	if spec.OrderPriceRound <= 0 {
		return "", fmt.Errorf("gate order_price_round 未采集 fail-closed")
	}
	pxR, err := roundToTick(px, spec.OrderPriceRound)
	if err != nil {
		return "", fmt.Errorf("gate post-only px %.6f: %w", px, err)
	}
	size := int64(math.Round(sizeF))
	if o.Side == "SELL" {
		size = -size
	}
	sizeLit := strconv.FormatInt(size, 10)
	if spec.EnableDecimal {
		sizeLit = strconv.FormatFloat(sizeF, 'f', -1, 64)
	}
	bodyStr := fmt.Sprintf(`{"contract":%q,"size":%s,"price":%q,"tif":"poc","text":%q,"reduce_only":false}`,
		o.Symbol, sizeLit, strconv.FormatFloat(pxR, 'f', -1, 64), gateOrderText(o))
	const endpoint = "/futures/usdt/orders"
	rawBody, err := e.gateSignedPost(ctx, endpoint, "/api/v4"+endpoint, bodyStr, true)
	if err != nil {
		return "", fmt.Errorf("gate post-only 拒单: %w", err)
	}
	var ord gateOrderResponse
	if err := json.Unmarshal(rawBody, &ord); err != nil {
		return "", fmt.Errorf("gate post-only json: %w", err)
	}
	if ord.Status == "finished" && ord.FinishAs != "filled" {
		// poc 即撤（会吃单）——不 chase，终态错误。
		return "", fmt.Errorf("gate post-only 即撤（会吃单）finish_as=%s", ord.FinishAs)
	}
	return strconv.FormatInt(ord.ID, 10), nil
}

// --- 撤单终查 + 等待 ---
