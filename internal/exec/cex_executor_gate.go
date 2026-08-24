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

// gateOrderResponse Gate futures 订单响应/状态对象字段（D-098 G1，返修包① M1/M2 对拍 gateapi-go）。
// 官方 SDK FuturesOrder：Id=**int64**（JSON number，非 string，unmarshal number→string 必炸）；
// Status 只 "open"/"finished" 两值；取消/IOC 未成交原因在 FinishAs（filled/cancelled/ioc/stp/
// liquidated/reduce_only/position_closed/auto_deleveraged/reduce_out）。
type gateOrderResponse struct {
	ID        int64  `json:"id"`         // 订单 ID（int64，JSON number——M1 对拍官方 SDK）
	Status    string `json:"status"`     // open / finished（无 canceled/rejected，M2）
	FinishAs  string `json:"finish_as"`  // filled / cancelled / ioc / stp / liquidated / ...（M2）
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
	_, fill, err := e.placeGateRaw(ctx, o)
	return fill, err
}

// GatePlaceOrderRaw 下单并返回 POST 原始响应 body（探针形状证据用，D-098 返修包① M4②）。
// 与 PlaceOrder→placeGate 同路径，额外暴露原始 body 供一次性诊断工具打印真实 API 形状
// （本次三形状缺陷全因 mock 证明不了真实形状——一次性探针直接打原始响应）。
func (e *CEXExecutor) GatePlaceOrderRaw(ctx context.Context, o Order) ([]byte, Fill, error) {
	return e.placeGateRaw(ctx, o)
}

// placeGateRaw placeGate 的核心实现，额外返回 POST 原始响应 body（M4② 探针形状证据）。
func (e *CEXExecutor) placeGateRaw(ctx context.Context, o Order) ([]byte, Fill, error) {
	if e.venueType != "perp" {
		return nil, Fill{}, fmt.Errorf("gate executor: venueType %q unsupported（G1 仅 perp）", e.venueType)
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
	rawBody, err := e.post(ctx, e.baseURL+endpoint, bodyStr, headers)
	if err != nil {
		return nil, Fill{}, fmt.Errorf("gate order: %w", err)
	}
	var ord gateOrderResponse
	if err := json.Unmarshal(rawBody, &ord); err != nil {
		return rawBody, Fill{}, fmt.Errorf("gate order json: %w", err)
	}
	// 成交确认（对齐 okx/binance 教训，防记 0 qty，M2 重排按 finish_as 判定）：
	// gate status 只 open/finished；status=open → 轮询 GET /futures/usdt/orders/{order_id} 至终态；
	// 终态（finished）按 finish_as 判定（gateFinishFill）。订单被拒/取消 = finish_as!=filled 且
	// qty==0 → gateFinishFill 报错（无 canceled/rejected status，旧判定为死代码已删）。
	if ord.Status == "open" {
		var last gateOrderResponse
		for range 5 {
			time.Sleep(100 * time.Millisecond)
			st, perr := e.gateOrderStatus(ctx, strconv.FormatInt(ord.ID, 10))
			if perr != nil {
				return rawBody, Fill{}, fmt.Errorf("gate order poll: %w", perr)
			}
			if st.Status == "finished" {
				fill, ferr := gateFinishFill(st)
				return rawBody, fill, ferr
			}
			last = st
		}
		// 5 次轮询仍 open → fallthrough 守卫（防 0-qty 假成交，okx 教训，M2）
		qty, price := gateFilled(last)
		if qty <= 0 || price <= 0 {
			return rawBody, Fill{}, fmt.Errorf("gate order %d: 5 次轮询仍 open 且无成交（qty=%v price=%v left=%d）",
				ord.ID, qty, price, last.Left)
		}
		return rawBody, Fill{OrderID: strconv.FormatInt(last.ID, 10), Price: price, Qty: qty, Ts: time.Now()}, nil
	}
	// POST 即 finished（IOC 常见：finish_as=ioc/filled/cancelled/stp/...）
	fill, ferr := gateFinishFill(ord)
	return rawBody, fill, ferr
}

// gateFinishFill 按 finish_as 判定终态订单成交（D-098 返修包① M2）：
//   - finish_as=="filled" → 全成交 Fill（qty=|size-left|，filled 时 left=0 即 |size|）；
//   - finish_as!="filled"（ioc/cancelled/stp/liquidated/reduce_only/position_closed/...）：
//     qty=|size-left|>0 → 如实部分成交 Fill；qty==0 → error 含 finish_as（未成交，防 0-qty 假成交）。
//
// okx 教训：IOC 零成交（finished+finish_as=ioc+left=size）旧逻辑落入 fallthrough 返
// Fill{Qty:0,Price:0},nil → executeLeg 记 FILLED 0-qty 腿。本函数守卫 qty==0 → error。
func gateFinishFill(o gateOrderResponse) (Fill, error) {
	qty, price := gateFilled(o)
	id := strconv.FormatInt(o.ID, 10)
	if o.FinishAs == "filled" {
		return Fill{OrderID: id, Price: price, Qty: qty, Ts: time.Now()}, nil
	}
	if qty > 0 {
		return Fill{OrderID: id, Price: price, Qty: qty, Ts: time.Now()}, nil
	}
	// qty==0：未成交（IOC 零成交 / cancelled / stp / liquidated / ...）→ 如实报错含 finish_as
	return Fill{}, fmt.Errorf("gate order %d: finish_as=%s 未成交（size=%d left=%d）",
		o.ID, o.FinishAs, o.Size, o.Left)
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
// 返修包① M3 对拍 gateapi-go：GetPosition 返回**单个 Position 对象**（非数组，ListPositions 才是数组），
// 旧代码 unmarshal 到 []struct 对真实 API 恒失败。改为单对象；size==0 → 返回空切片不误报。
func (e *CEXExecutor) GatePositions(ctx context.Context, contract string) ([]GatePosition, error) {
	_, poss, err := e.GatePositionsRaw(ctx, contract)
	return poss, err
}

// GatePositionsRaw 同 GatePositions，额外返回 GET 原始响应 body（D-098 返修② 探针形状证据：
// 一次运行定案 funding 字段是否真实存在于 positions 响应 JSON，与 account_book 双保险对拍）。
func (e *CEXExecutor) GatePositionsRaw(ctx context.Context, contract string) ([]byte, []GatePosition, error) {
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
		return nil, nil, fmt.Errorf("gate positions: %w", err)
	}
	var p struct {
		Contract   string `json:"contract"`
		Size       int64  `json:"size"`
		EntryPrice string `json:"entry_price"`
		MarkPrice  string `json:"mark_price"`
		Funding    string `json:"funding"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return body, nil, fmt.Errorf("gate positions json: %w", err)
	}
	if p.Size == 0 {
		return body, nil, nil // 无仓不误报
	}
	ep, _ := strconv.ParseFloat(p.EntryPrice, 64)
	mp, _ := strconv.ParseFloat(p.MarkPrice, 64)
	fd, _ := strconv.ParseFloat(p.Funding, 64)
	return body, []GatePosition{{
		Contract: p.Contract, Size: p.Size,
		EntryPrice: ep, MarkPrice: mp, Funding: fd,
	}}, nil
}

// GateAccountBookEntry Gate USDT 本位合约资金账本一行（D-098 返修② 探针结算真实性证据）。
// GET /futures/usdt/account_book?type=fund&contract={c} 返回真实结算行：
// text="funding"=资金费率结算，change=资金变动（字符串），time=unix 秒，refid=关联 ID。
// 生产零消费方（仅探针用），不复用 GatePosition.Funding（SDK/文档双证据指向不存在）。
type GateAccountBookEntry struct {
	Time   int64  `json:"time"`   // unix 秒
	Change string `json:"change"` // 资金变动（字符串，负=支付/正=收取）
	RefID  string `json:"refid"`  // 关联 ID
	Text   string `json:"text"`   // 业务类型（funding=资金费率结算）
}

// GateAccountBookFund 查询 Gate 指定 contract 的资金费率结算账本（GET /futures/usdt/account_book?type=fund&contract={c}，签名 GET 带 query）。
// 返回原始 body（探针形状证据）+ 解析行。query 进签名（GateSign 第三段=原始 query string）。
func (e *CEXExecutor) GateAccountBookFund(ctx context.Context, contract string) ([]byte, []GateAccountBookEntry, error) {
	endpoint := "/futures/usdt/account_book"
	query := "type=fund&contract=" + contract
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := GateSign(e.apiSecret, "GET", urlPath, query, "", ts)
	headers := http.Header{
		"KEY":       {e.apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {sign},
	}
	body, err := e.get(ctx, e.baseURL+endpoint+"?"+query, headers)
	if err != nil {
		return nil, nil, fmt.Errorf("gate account_book: %w", err)
	}
	var entries []GateAccountBookEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return body, nil, fmt.Errorf("gate account_book json: %w", err)
	}
	return body, entries, nil
}
