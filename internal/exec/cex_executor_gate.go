package exec

import (
	"arb/internal/signutil"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"
)

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
	// D-120：gate futures size 单位=合约数，o.Qty 是基础币数量 → 查合约规格换算。
	// D-564 S4：gateSpecCheck 承接换算+前置闸（in_delisting/size min/max/整数张 floor）。
	sizeF, spec, err := e.gateSpecCheck(ctx, o.Symbol, o.Qty)
	if err != nil {
		return nil, Fill{}, fmt.Errorf("gate order size: %w", err)
	}
	size := int64(math.Round(sizeF))
	if o.Side == "SELL" {
		size = -size // SELL 腿=负（short；gate 契约语义，D-120 原形）
	}
	// gateFilled 返回的 qty 是合约数 → 末尾乘 quanto 回填为基础币数量（Fill.Qty 契约=基础币）。
	quanto := spec.Quanto
	// D-564 S1：杠杆纯读化——开仓前 SET 段退役（push 端点带切保证金模式副作用永久禁用）；
	// precheck 经 get_leverage 实读 venue 实况，读不到 fail-closed 拒开。
	// D-121：平仓（PositionSide!=""）——D-564 S3 U4 dual 模式分叉：dual/dual_plus 官方
	// 机制=auto_size=close_long|close_short（size=0 全平；reduce_only 在 dual 语义未定义
	// 文档明示单仓专用）；single 维持 reduce_only=true。模式每次实读 accounts（禁缓存）。
	var bodyStr string
	closeDual := false
	if o.PositionSide != "" {
		mode, merr := e.gatePositionModeFetch(ctx)
		if merr != nil {
			return nil, Fill{}, fmt.Errorf("gate close order mode: %w", merr)
		}
		closeDual = mode.isDual() // D-564：每次实读（禁缓存——app 可切模式）
	}
	if closeDual {
		closeSide := "close_long"
		if o.Side == "SELL" {
			closeSide = "close_short"
		}
		bodyStr = fmt.Sprintf(`{"contract":%q,"size":0,"price":"0","tif":"ioc","text":"t-arb","close":true,"auto_size":%q}`,
			o.Symbol, closeSide)
	} else {
		// D-564 S4：size 序列化按 enable_decimal 分支——false=整数张 %d（现形）；
		// true=小数字符串（strip 零尾）。开仓带 reduce_only=false，平仓 reduce_only=true。
		sizeLit := strconv.FormatInt(size, 10)
		if spec.EnableDecimal {
			sizeLit = strconv.FormatFloat(sizeF, 'f', -1, 64)
		}
		bodyStr = fmt.Sprintf(`{"contract":%q,"size":%s,"price":"0","tif":"ioc","text":"t-arb","reduce_only":%t}`,
			o.Symbol, sizeLit, o.PositionSide != "")
	}

	const endpoint = "/futures/usdt/orders"
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := signutil.GateSign(e.apiSecret, "POST", urlPath, "", bodyStr, ts)
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
	// D-120：gateFilled 返回的 qty 是合约数 → 乘 quanto 回填为基础币数量（Fill.Qty 契约=基础币）。
	toBaseQty := func(fill Fill) Fill { fill.Qty = fill.Qty * quanto; return fill }
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
				if ferr != nil {
					return rawBody, fill, ferr
				}
				return rawBody, toBaseQty(fill), nil
			}
			last = st
		}
		// 5 次轮询仍 open → fallthrough 守卫（防 0-qty 假成交，okx 教训，M2）
		qty, price, ferr := gateFilled(last)
		if ferr != nil {
			return rawBody, Fill{}, ferr
		}
		if qty <= 0 || price <= 0 {
			return rawBody, Fill{}, fmt.Errorf("gate order %d: 5 次轮询仍 open 且无成交（qty=%v price=%v left=%d）",
				ord.ID, qty, price, last.Left)
		}
		return rawBody, toBaseQty(Fill{OrderID: strconv.FormatInt(last.ID, 10), Price: price, Qty: qty, Ts: time.Now()}), nil
	}
	// POST 即 finished（IOC 常见：finish_as=ioc/filled/cancelled/stp/...）
	fill, ferr := gateFinishFill(ord)
	if ferr != nil {
		return rawBody, fill, ferr
	}
	return rawBody, toBaseQty(fill), nil
}

// gateOrderStatus 查询 Gate 订单成交（GET /futures/usdt/orders/{order_id}，签名 GET）。
func (e *CEXExecutor) gateOrderStatus(ctx context.Context, orderID string) (gateOrderResponse, error) {
	endpoint := "/futures/usdt/orders/" + orderID
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := signutil.GateSign(e.apiSecret, "GET", urlPath, "", "", ts)
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

// GatePositions 查当前持仓（GET /futures/usdt/positions/{contract}）。
// M3 对拍：GetPosition 返单对象非数组（旧 []struct 恒失败）；size==0 → 空切片。
func (e *CEXExecutor) GatePositions(ctx context.Context, contract string) ([]GatePosition, error) {
	_, poss, err := e.GatePositionsRaw(ctx, contract)
	return poss, err
}

// GateAccountBookEntry gate 资金账本行（D-098 探针证据；生产零消费方仅探针用）。
type GateAccountBookEntry struct {
	Time    float64 `json:"time"`     // unix 秒（float64 带小数，对拍官方模型 FuturesAccountBook.Time=float64，真实响应样本 1682294400.123456）
	Change  string  `json:"change"`   // 资金变动（字符串，负=支付/正=收取）
	TradeID string  `json:"trade_id"` // 关联交易 ID（对拍官方模型 trade_id，真实响应无 refid 字段）
	Text    string  `json:"text"`     // 业务类型注释（真实格式 "合约:交易ID"，判定不依赖 text 内容）
}

// GateAccountBookFund 查询 Gate 指定 contract 的资金费率结算账本（GET /futures/usdt/account_book?type=fund&contract={c}，签名 GET 带 query）。
// 返回原始 body（探针形状证据）+ 解析行。query 进签名（GateSign 第三段=原始 query string）。
func (e *CEXExecutor) GateAccountBookFund(ctx context.Context, contract string) ([]byte, []GateAccountBookEntry, error) {
	endpoint := "/futures/usdt/account_book"
	query := "type=fund&contract=" + contract
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := signutil.GateSign(e.apiSecret, "GET", urlPath, query, "", ts)
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

// gateContractSize 查合约规格换算合约数（D-120）：size=round(qty/quanto_multiplier)。
// 公开端点 GET /futures/usdt/contracts/{contract}（签名头复用 e.get）；
// quanto per-executor 缓存；返回 (size, quanto, error)——quanto 回填 Fill.Qty 基础币量。
// gateContractSpec D-564 S4 U6：合约规格消费面（官方 Contract 模型字段子集）。
type gateContractSpec struct {
	Quanto        float64 // quanto_multiplier（每张基础币量）
	SizeMin       float64 // order_size_min（最小张数）
	SizeMax       float64 // order_size_max（0=未采集不拦）
	EnableDecimal bool    // true=size 可小数串；false=整数张
	InDelisting   bool    // 退市中→fail-closed
	LeverageMax   float64 // 矛盾校验用（venue 实况 lever>max → warn 不拦）
}

// gateSpecFetch 查合约规格并写缓存（TTL=进程生命周期——规格慢变元数据）。
func (e *CEXExecutor) gateSpecFetch(ctx context.Context, contract string) (gateContractSpec, error) {
	if e.gateSpec != nil {
		if s, ok := e.gateSpec[contract]; ok {
			return s, nil
		}
	}
	endpoint := "/futures/usdt/contracts/" + contract
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := signutil.GateSign(e.apiSecret, "GET", urlPath, "", "", ts)
	body, err := e.get(ctx, e.baseURL+endpoint, http.Header{
		"KEY": {e.apiKey}, "Timestamp": {strconv.FormatInt(ts, 10)}, "SIGN": {sign},
	})
	if err != nil {
		return gateContractSpec{}, fmt.Errorf("gate contract spec %s: %w", contract, err)
	}
	var spec struct {
		QuantoMultiplier string  `json:"quanto_multiplier"`
		OrderSizeMin     float64 `json:"order_size_min"`
		OrderSizeMax     float64 `json:"order_size_max"`
		EnableDecimal    bool    `json:"enable_decimal"`
		InDelisting      bool    `json:"in_delisting"`
		LeverageMax      float64 `json:"leverage_max"`
	}
	if err := json.Unmarshal(body, &spec); err != nil {
		return gateContractSpec{}, fmt.Errorf("gate contract spec json: %w", err)
	}
	q, err := f64z(spec.QuantoMultiplier)
	if err != nil || q <= 0 {
		return gateContractSpec{}, fmt.Errorf("gate contract %s quanto_multiplier=%q invalid", contract, spec.QuantoMultiplier)
	}
	s := gateContractSpec{
		Quanto: q, SizeMin: spec.OrderSizeMin, SizeMax: spec.OrderSizeMax,
		EnableDecimal: spec.EnableDecimal, InDelisting: spec.InDelisting, LeverageMax: spec.LeverageMax,
	}
	if e.gateSpec == nil {
		e.gateSpec = make(map[string]gateContractSpec)
	}
	e.gateSpec[contract] = s
	if e.gateQuanto == nil {
		e.gateQuanto = make(map[string]float64)
	}
	e.gateQuanto[contract] = q
	return s, nil
}

// gateSpecCheck D-564 S4 前置闸：退市/最小张数/最大张数/整数张 floor。
// size=qty/quanto 张数；enable_decimal=false 时 floor 到整数张（floor 后=0 拒）。
// 返回（生效张数, spec, error）。
func (e *CEXExecutor) gateSpecCheck(ctx context.Context, contract string, qty float64) (float64, gateContractSpec, error) {
	spec, err := e.gateSpecFetch(ctx, contract)
	if err != nil {
		return 0, spec, err
	}
	if spec.InDelisting {
		return 0, spec, fmt.Errorf("gate %s 合约退市中（in_delisting）fail-closed 拒单", contract)
	}
	size := qty / spec.Quanto
	if spec.SizeMin > 0 && size < spec.SizeMin {
		return 0, spec, fmt.Errorf("gate %s 不足最小张数：%.4f < order_size_min %.4f fail-closed", contract, size, spec.SizeMin)
	}
	if spec.SizeMax > 0 && size > spec.SizeMax {
		return 0, spec, fmt.Errorf("gate %s 超最大张数：%.4f > order_size_max %.4f fail-closed", contract, size, spec.SizeMax)
	}
	if !spec.EnableDecimal {
		floored := math.Floor(size)
		if floored < 1 {
			return 0, spec, fmt.Errorf("gate %s 整数张合约 qty %.6f（%.2f 张）floor 后=0 拒单", contract, qty, size)
		}
		size = floored
	}
	return size, spec, nil
}

func (e *CEXExecutor) gateContractSize(ctx context.Context, contract string, qty float64) (int64, float64, error) {
	// 缓存命中
	if e.gateQuanto != nil {
		if q, ok := e.gateQuanto[contract]; ok {
			s := int64(math.Round(qty / q))
			if s < 1 {
				return 0, 0, fmt.Errorf("qty %.6f / quanto %.6f = %.0f < 1 张（gate 最小 1 张）", qty, q, math.Round(qty/q))
			}
			return s, q, nil
		}
	}
	// 查合约规格
	endpoint := "/futures/usdt/contracts/" + contract
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := signutil.GateSign(e.apiSecret, "GET", urlPath, "", "", ts)
	headers := http.Header{
		"KEY":       {e.apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {sign},
	}
	body, err := e.get(ctx, e.baseURL+endpoint, headers)
	if err != nil {
		return 0, 0, fmt.Errorf("gate contract spec %s: %w", contract, err)
	}
	var spec struct {
		QuantoMultiplier string `json:"quanto_multiplier"`
	}
	if err := json.Unmarshal(body, &spec); err != nil {
		return 0, 0, fmt.Errorf("gate contract spec json: %w", err)
	}
	q, err := f64z(spec.QuantoMultiplier)
	if err != nil || q <= 0 {
		return 0, 0, fmt.Errorf("gate contract %s quanto_multiplier=%q invalid", contract, spec.QuantoMultiplier)
	}
	// 写缓存
	if e.gateQuanto == nil {
		e.gateQuanto = make(map[string]float64)
	}
	e.gateQuanto[contract] = q
	s := int64(math.Round(qty / q))
	if s < 1 {
		return 0, 0, fmt.Errorf("qty %.6f / quanto %.6f = %.0f < 1 张（gate 最小 1 张）", qty, q, math.Round(qty/q))
	}
	return s, q, nil
}
