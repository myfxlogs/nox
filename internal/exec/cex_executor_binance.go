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
	params := url.Values{}
	params.Set("symbol", o.Symbol)
	params.Set("side", side)
	params.Set("type", "MARKET")
	params.Set("quantity", strconv.FormatFloat(o.Qty, 'f', -1, 64))

	endpoint := "/api/v3/order"
	if e.venueType == "perp" {
		endpoint = "/fapi/v1/order"
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

	qty, _ := strconv.ParseFloat(execType, 64)
	avgPrice := binanceOrderAvgPrice(body, qty)

	if e.venueType == "perp" && (qty <= 0 || avgPrice <= 0) {
		// 合约市价单 POST 可能先于撮合返回（status=NEW/executedQty=0，demo-fapi 实测恒如此，
		// 同 D-059 实施修正② 的 OKX 教训；spot 市价单响应同步成交，无需轮询）→ 轮询订单状态
		// 拿真实成交，防生产记 0 qty 持仓。
		for range 5 {
			time.Sleep(100 * time.Millisecond)
			st, err := e.binanceOrderStatus(ctx, o.Symbol, string(orderID))
			if err != nil {
				return Fill{}, err
			}
			if st.Status == "CANCELED" || st.Status == "REJECTED" || st.Status == "EXPIRED" {
				// 未成交即被取消/拒绝 → 平仓失败，如实报错而非返回 qty=0 假装成交。
				msg := fmt.Sprintf("binance order %s: status=%s executedQty=%s（未成交，订单被取消/拒绝）",
					orderID, st.Status, st.ExecutedQty)
				e.alertCritical(o, msg)
				return Fill{}, fmt.Errorf("%s", msg)
			}
			if st.Status == "FILLED" || st.Status == "PARTIALLY_FILLED" {
				q, perr := strconv.ParseFloat(st.ExecutedQty, 64)
				p, aerr := strconv.ParseFloat(st.AvgPrice, 64)
				if perr == nil && aerr == nil && q > 0 && p > 0 {
					return Fill{OrderID: string(orderID), Price: p, Qty: q, Ts: time.Now()}, nil
				}
			}
		}
	}
	// 超时仍未拿到成交：如实返回已下订单 + 当前已知 qty（可 0），不伪造。
	return Fill{
		OrderID: string(orderID),
		Price:   avgPrice,
		Qty:     qty,
		Ts:      time.Now(),
	}, nil
}

// binanceOrderStatus 查询 Binance 订单成交（perp /fapi/v1/order；spot /api/v3/order，签名 GET）。
func (e *CEXExecutor) binanceOrderStatus(ctx context.Context, symbol, ordID string) (struct {
	Status      string `json:"status"`
	ExecutedQty string `json:"executedQty"`
	AvgPrice    string `json:"avgPrice"`
}, error) {
	endpoint := "/api/v3/order"
	if e.venueType == "perp" {
		endpoint = "/fapi/v1/order"
	}
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", ordID)
	body, err := e.signedBinanceRequest(ctx, "GET", endpoint, params)
	if err != nil {
		return struct {
			Status      string `json:"status"`
			ExecutedQty string `json:"executedQty"`
			AvgPrice    string `json:"avgPrice"`
		}{}, err
	}
	return struct {
		Status      string `json:"status"`
		ExecutedQty string `json:"executedQty"`
		AvgPrice    string `json:"avgPrice"`
	}{Status: asString(body["status"]), ExecutedQty: asString(body["executedQty"]), AvgPrice: asString(body["avgPrice"])}, nil
}

// binanceOrderAvgPrice 从 Binance 下单响应计算成交均价（D-059 实测：下单响应无 avgPrice 字段，
// 只有 fills 数组 + cummulativeQuoteQty；price 字段 MARKET 单恒 0。均价 = Σ(fill.price×qty)/Σqty，
// 无 fills 时回退 cummulativeQuoteQty/executedQty）。
func binanceOrderAvgPrice(body map[string]any, qty float64) float64 {
	if fills, ok := body["fills"].([]any); ok && len(fills) > 0 {
		var sumQty, sumVal float64
		for _, f := range fills {
			m, ok := f.(map[string]any)
			if !ok {
				continue
			}
			p, _ := strconv.ParseFloat(asString(m["price"]), 64)
			q, _ := strconv.ParseFloat(asString(m["qty"]), 64)
			sumVal += p * q
			sumQty += q
		}
		if sumQty > 0 {
			return sumVal / sumQty
		}
	}
	if cqqStr, ok := body["cummulativeQuoteQty"].(string); ok && qty > 0 {
		if cqq, err := strconv.ParseFloat(cqqStr, 64); err == nil {
			return cqq / qty
		}
	}
	return 0
}

// signedBinanceRequest 发 Binance 签名请求（HMAC hex + X-MBX-APIKEY）。
func (e *CEXExecutor) signedBinanceRequest(ctx context.Context, method, endpoint string, params url.Values) (map[string]any, error) {
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
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	return m, nil
}

// binanceSign HMAC-SHA256 hex 签名（与 reconcile 同法，各包独立实现——算法非事实，P3 不适用）。
func binanceSign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}
