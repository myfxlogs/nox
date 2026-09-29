package exec

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// placeBinance 下单：spot /api/v3/order、perp /fapi/v1/order，HMAC-SHA256 hex 签名 + X-MBX-APIKEY。
// D-108 P2 T1：perp 平仓补 positionSide（hedge 模式）/ reduceOnly（one-way 模式）。
func (e *CEXExecutor) placeBinance(ctx context.Context, o Order) (Fill, error) {
	side := "BUY"
	if o.Side == "SELL" {
		side = "SELL"
	}
	// D-564 S7 U10：perp 下单前本地闸——LOT_SIZE floor+minQty/MIN_NOTIONAL/maxNotional
	// fail-closed（脏量靠 venue 拒 -1111 改为本地预拦）。平仓（PositionSide!=""）只 floor
	// 不做名义闸（reduce 方向不新增敞口，名义闸语义仅开仓）。
	qty := o.Qty
	if e.venueType == "perp" {
		if o.PositionSide == "" {
			mark, merr := e.binanceMarkPrice(ctx, o.Symbol)
			if merr != nil {
				return Fill{}, fmt.Errorf("binance qty guard mark: %w", merr)
			}
			g, gerr := e.binanceQtyGuard(ctx, o.Symbol, o.Qty, mark)
			if gerr != nil {
				return Fill{}, gerr
			}
			qty = g
		} else {
			g, gerr := e.binanceQtyGuard(ctx, o.Symbol, o.Qty, 0)
			if gerr != nil {
				return Fill{}, gerr
			}
			qty = g
		}
	}
	params := url.Values{}
	params.Set("symbol", o.Symbol)
	params.Set("side", side)
	params.Set("type", "MARKET")
	params.Set("quantity", strconv.FormatFloat(qty, 'f', -1, 64))
	// D-195-B §4.3：client_order_id 透传（Binance 支持 newClientOrderId）。
	if o.ClientOrderID != "" {
		params.Set("newClientOrderId", o.ClientOrderID)
	}

	endpoint := "/api/v3/order"
	if e.venueType == "perp" {
		endpoint = "/v1/order"
		// D-108 P2 T1：perp 平仓补 positionSide/reduceOnly。
		// hedge 模式 → positionSide=原持仓侧（与 OKX 同机制，调用方传 Order.PositionSide）；
		// one-way 模式 → 平仓 reduceOnly=true（market reduce，防开反向敞口）。
		// 探测失败 → 保守按 hedge 处理（传 positionSide，被拒则 critical 告警）。
		// D-120 修复：开仓（PositionSide=""）不带 reduceOnly——开仓带 reduceOnly 会被拒
		// （Binance -2022 ReduceOnly Order is rejected，无持仓不能 reduce）。
		isClose := o.PositionSide != ""
		dual, probed := e.binanceDualSide(ctx)
		if !probed {
			// 探测失败：保守 hedge（有 positionSide 就传，无则不传让 venue 报错）
			if isClose {
				params.Set("positionSide", binancePosSide(o.PositionSide))
			}
		} else if dual {
			// hedge 模式：必须带 positionSide
			ps := o.PositionSide
			if ps == "" {
				ps = "long"
				if side == "SELL" {
					ps = "short"
				}
			}
			params.Set("positionSide", binancePosSide(ps))
		} else {
			// one-way 模式：仅平仓带 reduceOnly（开仓不带，否则 -2022 拒绝）
			if isClose {
				params.Set("reduceOnly", "true")
			}
		}
	}

	body, err := e.signedBinanceRequest(ctx, "POST", endpoint, params)
	if err != nil {
		e.alertCritical(o, fmt.Sprintf("binance perp close rejected pos=%d symbol=%s side=%s: %v", o.PosID, o.Symbol, side, err))
		return Fill{}, fmt.Errorf("cex order: %w", err)
	}

	execType, _ := body["executedQty"].(string)
	orderID, _ := body["orderId"].(json.Number)
	if orderID == "" {
		if id, ok := body["orderId"].(float64); ok {
			orderID = json.Number(strconv.FormatFloat(id, 'f', 0, 64))
		}
	}

	qty, perr := f64z(execType)
	if perr != nil {
		return Fill{}, fmt.Errorf("binance order %s executedQty=%q: %w", o.Symbol, execType, perr)
	}
	avgPrice, aerr := binanceOrderAvgPrice(body, qty)
	if aerr != nil {
		return Fill{}, aerr
	}

	if e.venueType == "perp" && (qty <= 0 || avgPrice <= 0) {
		// 合约市价单 POST 可能先于撮合返回（status=NEW/executedQty=0，实测恒如此，
		// 同 D-059 实施修正② 的 OKX 教训；spot 市价单响应同步成交，无需轮询）→ 退避轮询
		// 至 FILLED（D-421：PARTIALLY_FILLED 不提前返回——余量随后全成无记录=对冲失衡）；
		// 窗尽 cancel+终查；仍不可判 → ErrFillUnconfirmed。
		return e.binanceConfirmFill(ctx, o, string(orderID))
	}
	return Fill{
		OrderID: string(orderID),
		Price:   avgPrice,
		Qty:     qty,
		Ts:      time.Now(),
	}, nil
}

// binanceOrderStatusResult GET /fapi/v1/order（或 /api/v3/order）单条订单状态。
type binanceOrderStatusResult struct {
	Status      string `json:"status"`
	ExecutedQty string `json:"executedQty"`
	AvgPrice    string `json:"avgPrice"`
}

// binanceOrderStatus 查询 Binance 订单成交（perp /fapi/v1/order；spot /api/v3/order，签名 GET）。
// orderId 优先，空则 origClientOrderId 兜底（D-421 消解器按 clOrdId 查单凭据）。
func (e *CEXExecutor) binanceOrderStatus(ctx context.Context, symbol, ordID, clOrdID string) (binanceOrderStatusResult, error) {
	endpoint := "/api/v3/order"
	if e.venueType == "perp" {
		endpoint = "/v1/order"
	}
	params := url.Values{}
	params.Set("symbol", symbol)
	if ordID != "" {
		params.Set("orderId", ordID)
	} else {
		params.Set("origClientOrderId", clOrdID)
	}
	body, err := e.signedBinanceRequest(ctx, "GET", endpoint, params)
	if err != nil {
		return binanceOrderStatusResult{}, err
	}
	return binanceOrderStatusResult{
		Status:      asString(body["status"]),
		ExecutedQty: asString(body["executedQty"]),
		AvgPrice:    asString(body["avgPrice"]),
	}, nil
}

// binanceCancelOrder 撤销单笔订单（DELETE /fapi/v1/order，D-421 窗尽双证技巧）。
func (e *CEXExecutor) binanceCancelOrder(ctx context.Context, symbol, ordID string) error {
	endpoint := "/api/v3/order"
	if e.venueType == "perp" {
		endpoint = "/v1/order"
	}
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", ordID)
	_, err := e.signedBinanceRequest(ctx, "DELETE", endpoint, params)
	return err
}

// binanceOrderAvgPrice 从 Binance 下单响应计算成交均价（D-059 实测：下单响应无 avgPrice 字段，
// 只有 fills 数组 + cummulativeQuoteQty；price 字段 MARKET 单恒 0。均价 = Σ(fill.price×qty)/Σqty，
// 无 fills 时回退 cummulativeQuoteQty/executedQty）。
func binanceOrderAvgPrice(body map[string]any, qty float64) (float64, error) {
	if fills, ok := body["fills"].([]any); ok && len(fills) > 0 {
		var sumQty, sumVal float64
		for _, f := range fills {
			m, ok := f.(map[string]any)
			if !ok {
				continue
			}
			p, perr := f64z(asString(m["price"]))
			if perr != nil {
				return 0, fmt.Errorf("binance fill price=%q: %w", asString(m["price"]), perr)
			}
			q, qerr := f64z(asString(m["qty"]))
			if qerr != nil {
				return 0, fmt.Errorf("binance fill qty=%q: %w", asString(m["qty"]), qerr)
			}
			sumVal += p * q
			sumQty += q
		}
		if sumQty > 0 {
			return sumVal / sumQty, nil
		}
	}
	if cqqStr, ok := body["cummulativeQuoteQty"].(string); ok && qty > 0 {
		if cqq, err := f64z(cqqStr); err == nil {
			return cqq / qty, nil
		}
	}
	return 0, nil
}

// signedBinanceRequestRaw 发 Binance 签名请求（HMAC hex + X-MBX-APIKEY）返回原始 body。
// 签名/timestamp/Header/状态码检查公共核（D-427 I3：map/list 两解共享，不复制签名）。
func (e *CEXExecutor) signedBinanceRequestRaw(ctx context.Context, method, endpoint string, params url.Values) ([]byte, error) {
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")

	signature := binanceSign(e.apiSecret, params.Encode())
	params.Set("signature", signature)

	u := e.baseURL + endpoint + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-MBX-APIKEY", e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// signedBinanceRequest 发 Binance 签名请求，响应解 map。
func (e *CEXExecutor) signedBinanceRequest(ctx context.Context, method, endpoint string, params url.Values) (map[string]any, error) {
	data, err := e.signedBinanceRequestRaw(ctx, method, endpoint, params)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	return m, nil
}

// signedBinanceRequestList 同上，响应解数组（/v1/userTrades 等列表端点，D-427 I3）。
func (e *CEXExecutor) signedBinanceRequestList(ctx context.Context, method, endpoint string, params url.Values) ([]map[string]any, error) {
	data, err := e.signedBinanceRequestRaw(ctx, method, endpoint, params)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	return rows, nil
}

// binanceUserTradesFeeUSD 聚合某订单 userTrades 的 USDT 手续费（D-427 I3）：
// GET /fapi/v1/userTrades?symbol=&orderId=（perp；spot /api/v3/myTrades）。
// commissionAsset=USDT 行累加，非 USDT 行跳过不计；realizedPnl 字段不计（费≠盈亏）。
func (e *CEXExecutor) binanceUserTradesFeeUSD(ctx context.Context, symbol, ordID string) (float64, error) {
	endpoint := "/api/v3/myTrades"
	if e.venueType == "perp" {
		endpoint = "/v1/userTrades"
	}
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", ordID)
	rows, err := e.signedBinanceRequestList(ctx, "GET", endpoint, params)
	if err != nil {
		return 0, err
	}
	var sum float64
	for _, row := range rows {
		if asString(row["commissionAsset"]) != "USDT" {
			continue // 费币混杂如实缺（不猜换算）
		}
		c, cerr := f64z(asString(row["commission"]))
		if cerr != nil {
			return 0, fmt.Errorf("binance commission=%q: %w", asString(row["commission"]), cerr)
		}
		sum += c
	}
	return sum, nil
}

// binanceSign HMAC-SHA256 hex 签名（与 reconcile 同法，各包独立实现——算法非事实，P3 不适用）。
func binanceSign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}
