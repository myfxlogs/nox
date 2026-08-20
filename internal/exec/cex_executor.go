package exec

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"arb/internal/httpx"
)

// CEXExecutor 是 Binance/OKX REST 签名下单执行器（§13.2）。
// 每个市场一个实例（spot/perp 是不同实例）。D-059：补 OKX 下单（OK-ACCESS-* 四头 +
// base64 HMAC + x-simulated-trading:1 模拟盘），venue 分派。
type CEXExecutor struct {
	venue      string
	venueType  string // spot / perp
	apiKey     string
	apiSecret  string
	passphrase string // OKX 必填；Binance 空
	baseURL    string // binance: proxy/demo 根；okx: www.okx.com
	simulated  bool   // OKX 模拟盘：请求带 x-simulated-trading:1（demo key 必开，D-054 同款）
	client     *http.Client
}

// NewCEXExecutor 创建 CEX 执行器。
// baseURL 为 venue 根（binance 走代理 fapi 风格需已含 /demo 路径；okx 实盘域名）。
// simulated 仅 OKX 生效（Binance demo 由 baseURL 的 /demo 前缀路由，D-055）。
func NewCEXExecutor(venue, venueType, apiKey, apiSecret, passphrase, baseURL string, simulated bool) *CEXExecutor {
	return &CEXExecutor{
		venue:      venue,
		venueType:  venueType,
		apiKey:     apiKey,
		apiSecret:  apiSecret,
		passphrase: passphrase,
		baseURL:    baseURL,
		simulated:  simulated,
		client:     httpx.Client(), // CA 信任 + X-Proxy-Token 注入（D-055）；OKX 公网忽略注入头
	}
}

func (e *CEXExecutor) Key() string { return e.venue + ":" + e.venueType }

func (e *CEXExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	switch e.venue {
	case "binance":
		return e.placeBinance(ctx, o)
	case "okx":
		return e.placeOKX(ctx, o)
	default:
		return Fill{}, fmt.Errorf("cex executor: unsupported venue %q", e.venue)
	}
}

// placeBinance 下单：spot /api/v3/order、perp /fapi/v1/order，HMAC-SHA256 hex 签名 + X-MBX-APIKEY。
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
	}

	body, err := e.signedBinanceRequest(ctx, "POST", endpoint, params)
	if err != nil {
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
				return Fill{}, fmt.Errorf("binance order %s: status=%s executedQty=%s（未成交，订单被取消/拒绝）",
					orderID, st.Status, st.ExecutedQty)
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

// asString 容错取 string 字段（json.Number / float64 / string 都接受）。
func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

// placeOKX 下单：POST /api/v5/trade/order（OK-ACCESS-* 四头，SIGN=base64(HMAC-SHA256(secret,
// ts+method+path+body))）。spot tdMode=cash；perp tdMode=cross（代码保留，本次不验证）。
// 模拟盘带 x-simulated-trading:1（OKX demo key 必开，D-054 同款）。
func (e *CEXExecutor) placeOKX(ctx context.Context, o Order) (Fill, error) {
	side := "buy"
	if o.Side == "SELL" {
		side = "sell"
	}
	tdMode := "cash" // spot
	instID := o.Symbol
	if e.venueType == "perp" {
		tdMode = "cross"
		if !strings.HasSuffix(instID, "-SWAP") {
			instID = instID + "-SWAP" // OKX perp instId 形如 BTC-USDT-SWAP
		}
	}
	sz := strconv.FormatFloat(o.Qty, 'f', -1, 64)
	var bodyStr string
	if e.venueType == "perp" {
		// swap/futures 必须带 posSide（D-059 实施修正② 实测 51000）。本账户 posMode=long_short_mode
		// （account/config 实测）。开仓传空 → buy=long / sell=short 派生；平仓由调用方显式传
		// PositionSide=原持仓侧（BUY 开 long → SELL 平 + posSide=long 才减 long，D-059 实测
		// SELL+posSide=short 会新开空仓）。net_mode 账户需改 posSide=net。
		posSide := o.PositionSide
		if posSide == "" {
			posSide = "long"
			if side == "sell" {
				posSide = "short"
			}
		}
		bodyStr = fmt.Sprintf(`{"instId":%q,"tdMode":%q,"side":%q,"ordType":"market","sz":%q,"posSide":%q}`,
			instID, tdMode, side, sz, posSide)
	} else {
		// spot 市价单：缺省买方向 sz=报价币金额（0.0001 当 0.0001 USDT → 51020 最小金额，实测）。
		// 显式 tgtCcy=base_ccy 固定 sz=基础币数量，与 exec.Order.Qty 契约一致。
		bodyStr = fmt.Sprintf(`{"instId":%q,"tdMode":%q,"side":%q,"ordType":"market","sz":%q,"tgtCcy":"base_ccy"}`,
			instID, tdMode, side, sz)
	}

	path := "/api/v5/trade/order"
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	prehash := ts + "POST" + path + bodyStr
	sign := okxSign(e.apiSecret, prehash)

	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
		"Content-Type":         {"application/json"},
	}
	if e.simulated {
		headers.Set("x-simulated-trading", "1")
	}

	body, err := e.post(ctx, e.baseURL+path, bodyStr, headers)
	if err != nil {
		return Fill{}, fmt.Errorf("okx order: %w", err)
	}
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			OrdID     string `json:"ordId"`
			SCode     string `json:"sCode"`
			SMsg      string `json:"sMsg"`
			AvgPx     string `json:"avgPx"`
			AccFillSz string `json:"accFillSz"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return Fill{}, fmt.Errorf("okx order json: %w", err)
	}
	if resp.Code != "0" {
		// code=1「All operations failed」是包装错误，真实原因在 data[0].sCode/sMsg（D-059 实测）。
		detail := resp.Msg
		if len(resp.Data) > 0 && resp.Data[0].SMsg != "" {
			detail = fmt.Sprintf("%s (sCode=%s sMsg=%s)", resp.Msg, resp.Data[0].SCode, resp.Data[0].SMsg)
		}
		return Fill{}, fmt.Errorf("okx order: code=%s msg=%s", resp.Code, detail)
	}
	if len(resp.Data) == 0 {
		return Fill{}, fmt.Errorf("okx order: empty data")
	}
	d := resp.Data[0]
	if d.SCode != "" && d.SCode != "0" {
		return Fill{}, fmt.Errorf("okx order: sCode=%s sMsg=%s", d.SCode, d.SMsg)
	}
	qty, _ := strconv.ParseFloat(d.AccFillSz, 64)
	avgPx, _ := strconv.ParseFloat(d.AvgPx, 64)
	if qty <= 0 || avgPx <= 0 {
		// 市价单 POST 响应可能先于撮合返回（accFillSz=0/avgPx 空，D-059 实施修正② 实测：
		// 订单实际成交但响应无成交数据）→ 轮询订单状态拿真实成交，防生产记 0 qty 持仓。
		for range 5 {
			time.Sleep(100 * time.Millisecond)
			o, err := e.okxOrderStatus(ctx, instID, d.OrdID)
			if err != nil {
				return Fill{}, err
			}
			if o.State == "canceled" || o.State == "rejected" || o.State == "mmp_canceled" {
				// 订单未成交即被取消/拒绝 → 平仓失败，如实报错而非返回 qty=0 假装成交
				// （D-059 实测：spot SELL 曾因模拟盘撮合取消，旧逻辑静默返回 0 致测试假绿）。
				// cancelSourceReason 携带取消根因（如「滑超 best bid 5% 保护」），生产排障必读。
				reason := strings.TrimSpace(o.CancelSourceReason)
				if reason != "" {
					reason = ": " + reason
				}
				return Fill{}, fmt.Errorf("okx order %s: state=%s accFillSz=%s avgPx=%s（未成交，订单被取消/拒绝%s）",
					d.OrdID, o.State, o.AccFillSz, o.AvgPx, reason)
			}
			if o.AccFillSz != "" && o.AvgPx != "" {
				if q, err := strconv.ParseFloat(o.AccFillSz, 64); err == nil && q > 0 {
					qty = q
				}
				if p, err := strconv.ParseFloat(o.AvgPx, 64); err == nil && p > 0 {
					avgPx = p
				}
				if qty > 0 && avgPx > 0 {
					return Fill{OrderID: d.OrdID, Price: avgPx, Qty: qty, Ts: time.Now()}, nil
				}
			}
		}
	}
	// 超时仍未拿到成交：如实返回已下订单 + 当前已知 qty（可 0），不伪造。
	return Fill{OrderID: d.OrdID, Price: avgPx, Qty: qty, Ts: time.Now()}, nil
}

// okxOrderStatus 查询单笔订单成交（GET /api/v5/trade/order，签名四头 + simulated 头）。
func (e *CEXExecutor) okxOrderStatus(ctx context.Context, instID, ordID string) (struct {
	AccFillSz          string `json:"accFillSz"`
	AvgPx              string `json:"avgPx"`
	State              string `json:"state"`
	CancelSourceReason string `json:"cancelSourceReason"`
}, error) {
	var out struct {
		AccFillSz          string `json:"accFillSz"`
		AvgPx              string `json:"avgPx"`
		State              string `json:"state"`
		CancelSourceReason string `json:"cancelSourceReason"`
	}
	path := "/api/v5/trade/order?instId=" + url.QueryEscape(instID) + "&ordId=" + url.QueryEscape(ordID)
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	prehash := ts + "GET" + path
	sign := okxSign(e.apiSecret, prehash)
	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
	}
	if e.simulated {
		headers.Set("x-simulated-trading", "1")
	}
	body, err := e.get(ctx, e.baseURL+path, headers)
	if err != nil {
		return out, err
	}
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			AccFillSz          string `json:"accFillSz"`
			AvgPx              string `json:"avgPx"`
			State              string `json:"state"`
			CancelSourceReason string `json:"cancelSourceReason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return out, fmt.Errorf("okx order status json: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return out, fmt.Errorf("okx order status: code=%s", resp.Code)
	}
	d := resp.Data[0]
	out.AccFillSz, out.AvgPx, out.State, out.CancelSourceReason = d.AccFillSz, d.AvgPx, d.State, d.CancelSourceReason
	return out, nil
}

// OKXPosition 是一条 OKX 持仓（GET /api/v5/account/positions，D-059 实施修正② 自愈清理用）。
type OKXPosition struct {
	InstID  string
	PosSide string // long / short / net
	Pos     float64
	AvgPx   float64
}

// OKXPositions 查询 OKX 指定 instId 的当前持仓（long_short_mode 下 long/short 分列）。
// 供平仓核对 / demo 测试自愈清理（旧 bug 残留的双向持仓要能关掉），签名四头 + simulated 头。
func (e *CEXExecutor) OKXPositions(ctx context.Context, instID string) ([]OKXPosition, error) {
	path := "/api/v5/account/positions?instId=" + url.QueryEscape(instID)
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	prehash := ts + "GET" + path
	sign := okxSign(e.apiSecret, prehash)
	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
	}
	if e.simulated {
		headers.Set("x-simulated-trading", "1")
	}
	body, err := e.get(ctx, e.baseURL+path, headers)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			InstID  string `json:"instId"`
			PosSide string `json:"posSide"`
			Pos     string `json:"pos"`
			AvgPx   string `json:"avgPx"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("okx positions json: %w", err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("okx positions: code=%s msg=%s", resp.Code, resp.Msg)
	}
	var out []OKXPosition
	for _, d := range resp.Data {
		if d.InstID != instID {
			continue
		}
		p, _ := strconv.ParseFloat(d.Pos, 64)
		if p == 0 {
			continue
		}
		a, _ := strconv.ParseFloat(d.AvgPx, 64)
		out = append(out, OKXPosition{InstID: d.InstID, PosSide: d.PosSide, Pos: p, AvgPx: a})
	}
	return out, nil
}

func (e *CEXExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	// CEX 平仓 = 反向市价单
	symbol := ref.Symbol
	// 推断 side：CEX 没有"close"概念，需要外部传入方向
	// 简化：CloseOrder 在 CEX 场景由执行管线直接调 PlaceOrder 反向
	// 此方法保留接口完整性，实际 CEX 平仓走 PlaceOrder
	_ = symbol
	return Fill{}, fmt.Errorf("cex: use PlaceOrder with opposite side for close")
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

// get 发带签名头的 GET 请求并读回 body（非 200 → 错误带响应体）。
func (e *CEXExecutor) get(ctx context.Context, url string, headers http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
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

// post 发带签名头的 POST JSON 请求并读回 body（非 200 → 错误带响应体）。
func (e *CEXExecutor) post(ctx context.Context, url, bodyStr string, headers http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBufferString(bodyStr))
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
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

// binanceSign HMAC-SHA256 hex 签名（与 reconcile 同法，各包独立实现——算法非事实，P3 不适用）。
func binanceSign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}

// okxSign OKX 签名：base64(HMAC-SHA256(secret, ts+method+path+body))（与 reconcile 同法）。
func okxSign(secret, prehash string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(prehash))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
