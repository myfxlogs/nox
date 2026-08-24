package exec

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"
)

// === Gate（USDT 本位合约 perp，D-098 G1）===

// GateSign Gate API v4 官方签名（D-098 G1，事实已确证勿猜）：
//
//	sign_string = method + "\n" + url + "\n" + query + "\n" + payloadHash + "\n" + timestamp
//	  method      = HTTP 方法大写（POST/GET）
//	  url         = 路径含前缀 /api/v4 + endpoint（如 /api/v4/futures/usdt/orders）
//	  query       = 原始 query string（URL-encoded）；POST 无 query = ""
//	  payloadHash = hex(SHA512(body))；无 body（GET）时 SHA512("")
//	  timestamp   = unix 秒整数（int64），sign_string 与 header 用同一值
//	SIGN = hex(HMAC-SHA512(secret, sign_string))   // 注意是 SHA512 不是 SHA256
//
// 导出自由函数：exec.PlaceOrder（R1.1）与 reconcile.CEXAccountQuerier.accountGate（R1.5）共用，
// P3 一事实一处禁复制两份。reconcile 已 import exec（reconcile_queriers.go），无环。
func GateSign(secret, method, urlPath, query, body string, ts int64) string {
	payloadHash := hexSHA512(body)
	signString := fmt.Sprintf("%s\n%s\n%s\n%s\n%d", method, urlPath, query, payloadHash, ts)
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write([]byte(signString))
	return hex.EncodeToString(mac.Sum(nil))
}

// hexSHA512 返回 body 的 SHA512 十六进制摘要（空 body → SHA512("")，Gate 签名要求）。
func hexSHA512(body string) string {
	sum := sha512.Sum512([]byte(body))
	return hex.EncodeToString(sum[:])
}

// gateOrderResponse Gate futures 订单响应/状态对象字段（D-098 G1）。
type gateOrderResponse struct {
	ID        string `json:"id"`         // 订单 ID（字符串）
	Status    string `json:"status"`     // open / finished / canceled / ...
	Size      int64  `json:"size"`       // 原始委托量（带符号：正=买入/long，负=卖出/short）
	Left      int64  `json:"left"`       // 剩余未成交量
	FillPrice string `json:"fill_price"` // 成交均价（字符串）
}

// placeGate 下单：POST /futures/usdt/orders（HMAC-SHA512 签名，GateSign 共享）。
// contract = o.Symbol 原样透传（gate 腿 venue symbol 形如 BTC_USDT，gate_ws canonicalFromGate
// 已把 _ 转 canonical，resolveLegs 用 VenueSymbol 下单——与 okx 不同，无需拼 -SWAP）。
// size 语义：gate futures size>0=买入(long)、size<0=卖出(short)，单位为合约数。
// BUY leg → size=+o.Qty；SELL leg → size=-o.Qty。price=0 + tif=ioc = 市价（即时成交或取消）。
func (e *CEXExecutor) placeGate(ctx context.Context, o Order) (Fill, error) {
	if e.venueType != "perp" {
		return Fill{}, fmt.Errorf("gate executor: venueType %q unsupported（G1 仅 perp）", e.venueType)
	}
	size := int64(o.Qty)
	if o.Side == "SELL" {
		size = -size
	}
	bodyStr := fmt.Sprintf(`{"contract":%q,"size":%d,"price":"0","tif":"ioc","text":"t-arb"}`,
		o.Symbol, size)

	const endpoint = "/futures/usdt/orders"
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := GateSign(e.apiSecret, "POST", urlPath, "", bodyStr, ts)
	headers := http.Header{
		"KEY":          {e.apiKey},
		"Timestamp":    {strconv.FormatInt(ts, 10)},
		"SIGN":         {sign},
		"Content-Type": {"application/json"},
	}
	body, err := e.post(ctx, e.baseURL+endpoint, bodyStr, headers)
	if err != nil {
		return Fill{}, fmt.Errorf("gate order: %w", err)
	}
	var ord gateOrderResponse
	if err := json.Unmarshal(body, &ord); err != nil {
		return Fill{}, fmt.Errorf("gate order json: %w", err)
	}
	// 成交确认（对齐 okx/binance 教训，防记 0 qty）：status=open（部分/未成交）时轮询
	// GET /futures/usdt/orders/{order_id}，至多 5 次间隔 100ms；status=finished 且 left=0 → 全成交；
	// left>0 持续 → 如实返回已成交部分 qty=|size-left|；订单被拒/取消 → 如实报错含状态。
	if ord.Status == "open" || (ord.Status == "finished" && ord.Left != 0) {
		for range 5 {
			time.Sleep(100 * time.Millisecond)
			st, perr := e.gateOrderStatus(ctx, ord.ID)
			if perr != nil {
				return Fill{}, fmt.Errorf("gate order poll: %w", perr)
			}
			if st.Status == "canceled" || st.Status == "rejected" {
				return Fill{}, fmt.Errorf("gate order %s: status=%s left=%d（未成交，订单被取消/拒绝）",
					ord.ID, st.Status, st.Left)
			}
			if st.Status == "finished" {
				qty, price := gateFilled(st)
				if qty > 0 {
					return Fill{OrderID: ord.ID, Price: price, Qty: qty, Ts: time.Now()}, nil
				}
			}
		}
	}
	if ord.Status == "canceled" || ord.Status == "rejected" {
		return Fill{}, fmt.Errorf("gate order %s: status=%s left=%d（未成交，订单被取消/拒绝）",
			ord.ID, ord.Status, ord.Left)
	}
	qty, price := gateFilled(ord)
	return Fill{OrderID: ord.ID, Price: price, Qty: qty, Ts: time.Now()}, nil
}

// gateFilled 从订单对象计算已成交 qty（|size-left|）与均价（fill_price）。
func gateFilled(o gateOrderResponse) (float64, float64) {
	filled := o.Size - o.Left // 带符号，绝对值即成交合约数
	qty := math.Abs(float64(filled))
	price, _ := strconv.ParseFloat(o.FillPrice, 64)
	return qty, price
}

// gateOrderStatus 查询 Gate 订单成交（GET /futures/usdt/orders/{order_id}，签名 GET）。
func (e *CEXExecutor) gateOrderStatus(ctx context.Context, orderID string) (gateOrderResponse, error) {
	endpoint := "/futures/usdt/orders/" + orderID
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := GateSign(e.apiSecret, "GET", urlPath, "", "", ts)
	headers := http.Header{
		"KEY":       {e.apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {sign},
	}
	body, err := e.get(ctx, e.baseURL+endpoint, headers)
	if err != nil {
		return gateOrderResponse{}, err
	}
	var ord gateOrderResponse
	if err := json.Unmarshal(body, &ord); err != nil {
		return gateOrderResponse{}, fmt.Errorf("gate order status json: %w", err)
	}
	return ord, nil
}

// GatePosition 是一条 Gate USDT 本位合约持仓（D-098 G1，探针 + 平仓核对用）。
type GatePosition struct {
	Contract   string
	Size       int64 // >0 多头 / <0 空头 / 0 无仓
	EntryPrice float64
	MarkPrice  float64
	Funding    float64 // 累计已结算 funding（正=支付，负=收取；验证结算真实性读它）
}

// GatePositions 查询 Gate 指定 contract 的当前持仓（GET /futures/usdt/positions/{contract}，签名 GET）。
// 404/空仓时返回空切片（无仓不误报）。
func (e *CEXExecutor) GatePositions(ctx context.Context, contract string) ([]GatePosition, error) {
	endpoint := "/futures/usdt/positions/" + contract
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := GateSign(e.apiSecret, "GET", urlPath, "", "", ts)
	headers := http.Header{
		"KEY":       {e.apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {sign},
	}
	body, err := e.get(ctx, e.baseURL+endpoint, headers)
	if err != nil {
		return nil, fmt.Errorf("gate positions: %w", err)
	}
	var rows []struct {
		Contract   string `json:"contract"`
		Size       int64  `json:"size"`
		EntryPrice string `json:"entry_price"`
		MarkPrice  string `json:"mark_price"`
		Funding    string `json:"funding"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("gate positions json: %w", err)
	}
	var out []GatePosition
	for _, r := range rows {
		if r.Size == 0 {
			continue
		}
		ep, _ := strconv.ParseFloat(r.EntryPrice, 64)
		mp, _ := strconv.ParseFloat(r.MarkPrice, 64)
		fd, _ := strconv.ParseFloat(r.Funding, 64)
		out = append(out, GatePosition{
			Contract: r.Contract, Size: r.Size,
			EntryPrice: ep, MarkPrice: mp, Funding: fd,
		})
	}
	return out, nil
}
