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
	// D-564 S1：杠杆纯读化——开仓前 SET 段退役（push 端点带切保证金模式副作用永久禁用）；
	// precheck 经 get_leverage 实读 venue 实况，读不到 fail-closed 拒开。
	// D-121：平仓（PositionSide!=""）——D-564 S3 U4 dual 模式分叉：dual/dual_plus 官方
	// 机制=auto_size=close_long|close_short（size=0 全平；reduce_only 在 dual 语义未定义
	// 文档明示单仓专用）；single 维持 reduce_only=true。模式每次实读 accounts（禁缓存——
	// app 可切模式）。closeDual 分支 size 恒 0 → 豁免 spec qty 闸（min/max/整数张 floor
	// 不拦全平），spec 仅取 quanto 供 fill 量纲换算（R2）。
	var spec gateContractSpec
	var sizeF float64
	var err error
	closeDual := false
	if o.PositionSide != "" {
		mode, merr := e.gatePositionModeFetch(ctx)
		if merr != nil {
			return nil, Fill{}, fmt.Errorf("gate close order mode: %w", merr)
		}
		closeDual = mode.isDual()
	}
	if closeDual {
		if spec, err = e.gateSpecFetch(ctx, o.Symbol); err != nil {
			return nil, Fill{}, fmt.Errorf("gate contract spec: %w", err)
		}
	} else {
		// D-120：gate futures size 单位=合约数，o.Qty 是基础币数量 → 查合约规格换算。
		// D-564 S4：gateSpecCheck 承接换算+前置闸（in_delisting/size min/max/整数张 floor）。
		if sizeF, spec, err = e.gateSpecCheck(ctx, o.Symbol, o.Qty); err != nil {
			return nil, Fill{}, fmt.Errorf("gate order size: %w", err)
		}
	}
	size := int64(math.Round(sizeF))
	if o.Side == "SELL" {
		size = -size // SELL 腿=负（short；gate 契约语义，D-120 原形）
	}
	// gateFilled 返回的 qty 是合约数 → 末尾乘 quanto 回填为基础币数量（Fill.Qty 契约=基础币）。
	quanto := spec.Quanto
	var bodyStr string
	if closeDual {
		// R1：auto_size 按持仓侧（PositionSide=原持仓方向）驱动——long→close_long、
		// short→close_short；非法值 fail-closed（按下单侧 o.Side 推导曾把
		// 「SELL 平多」错写成 close_short）。
		var closeSide string
		switch o.PositionSide {
		case "long":
			closeSide = "close_long"
		case "short":
			closeSide = "close_short"
		default:
			return nil, Fill{}, fmt.Errorf("gate dual close: PositionSide %q 非法（long/short）fail-closed", o.PositionSide)
		}
		// D-570 包B S5：text 单号真实化（t-<clientOrderID> 截28B；空=t-arb 兜底）。
		bodyStr = fmt.Sprintf(`{"contract":%q,"size":0,"price":"0","tif":"ioc","text":%q,"close":true,"auto_size":%q,"reduce_only":true}`,
			o.Symbol, gateOrderText(o), closeSide)
	} else {
		// D-564 S4：size 序列化按 enable_decimal 分支——false=整数张 %d（现形）；
		// true=小数字符串（strip 零尾）。开仓带 reduce_only=false，平仓 reduce_only=true。
		sizeLit := strconv.FormatInt(size, 10)
		if spec.EnableDecimal {
			sizeLit = strconv.FormatFloat(sizeF, 'f', -1, 64)
		}
		bodyStr = fmt.Sprintf(`{"contract":%q,"size":%s,"price":"0","tif":"ioc","text":%q,"reduce_only":%t}`,
			o.Symbol, sizeLit, gateOrderText(o), o.PositionSide != "")
	}

	const endpoint = "/futures/usdt/orders"
	urlPath := "/api/v4" + endpoint
	// D-570 包A S2d：签名+发送入 resilience——REQUEST_EXPIRED → 校时后同 bodyStr/urlPath 重签重发一次。
	rawBody, err := e.gateSignedPost(ctx, endpoint, urlPath, bodyStr, true)
	if err != nil {
		// D-570 包C S6②：拒单驱动失效——规格类标签清本 contract 缓存（本单不重试按现状上抛）。
		if lab, ok := gateErrLabel(err); ok &&
			(lab == "INVALID_PRECISION" || lab == "AMOUNT_TOO_LITTLE" || lab == "AMOUNT_TOO_MUCH") {
			GateSpecInvalidate(o.Symbol)
		}
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
		// D-570 包B S4②：内联 5×100ms 轮询重构为 gateConfirmFill 三分量窗
		//（事件 lookup 先查+REST 兜底+窗尽撤单终查+ErrFillUnconfirmed）。
		// qty 换算责任钉死 gateConfirmFill 内部（返基础币），本调用点不再乘 quanto。
		fill, ferr := e.gateConfirmFill(ctx, o, strconv.FormatInt(ord.ID, 10))
		return rawBody, fill, ferr
	}
	// POST 即 finished（IOC 常见：finish_as=ioc/filled/cancelled/stp/...）
	fill, ferr := gateFinishFill(ord)
	if ferr != nil {
		return rawBody, fill, ferr
	}
	return rawBody, toBaseQty(fill), nil
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
	ts := gateNow()
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
	LeverageMax   float64 // 矛盾校验用（venue 实况 lever>max → warn 不拦；实弹=JSON 字符串 D-570 根治）
	// D-570 包A S1a：市价量上限+交易状态（实弹=JSON 字符串形，api-response-fields-empirical）。
	MarketOrderSizeMax float64   // market_order_size_max（0=未采集不拦；市价单语义闸）
	Status             string    // status（"trading" 放行；prelaunch/delisting/delisted/circuit_breaker 全拦）
	at                 time.Time // D-570 包C S6①：写入时刻（24h TTL 惰性判龄）
}

// gateSpecFetch 查合约规格并写缓存（D-570 包C S6①：包级 sync.Map+24h TTL 地板；
// 失效三通道=contract_info WS 推送/判龄/拒单驱动，同走 GateSpecInvalidate）。
func (e *CEXExecutor) gateSpecFetch(ctx context.Context, contract string) (gateContractSpec, error) {
	if v, ok := gateSpecCache.Load(contract); ok {
		if s, ok2 := v.(gateContractSpec); ok2 && time.Since(s.at) <= gateSpecTTL {
			return s, nil
		}
	}
	endpoint := "/futures/usdt/contracts/" + contract
	urlPath := "/api/v4" + endpoint
	ts := gateNow()
	sign := signutil.GateSign(e.apiSecret, "GET", urlPath, "", "", ts)
	body, err := e.get(ctx, e.baseURL+endpoint, http.Header{
		"KEY": {e.apiKey}, "Timestamp": {strconv.FormatInt(ts, 10)}, "SIGN": {sign},
	})
	if err != nil {
		return gateContractSpec{}, fmt.Errorf("gate contract spec %s: %w", contract, err)
	}
	var spec struct {
		QuantoMultiplier   string  `json:"quanto_multiplier"`
		OrderSizeMin       float64 `json:"order_size_min"`
		OrderSizeMax       float64 `json:"order_size_max"`
		EnableDecimal      bool    `json:"enable_decimal"`
		InDelisting        bool    `json:"in_delisting"`
		LeverageMax        string  `json:"leverage_max"`          // D-570 活病灶根治：实弹=JSON 字符串
		MarketOrderSizeMax string  `json:"market_order_size_max"` // 同
		Status             string  `json:"status"`
	}
	if err := json.Unmarshal(body, &spec); err != nil {
		return gateContractSpec{}, fmt.Errorf("gate contract spec json: %w", err)
	}
	q, err := f64z(spec.QuantoMultiplier)
	if err != nil || q <= 0 {
		return gateContractSpec{}, fmt.Errorf("gate contract %s quanto_multiplier=%q invalid", contract, spec.QuantoMultiplier)
	}
	lvMax, err := f64z(spec.LeverageMax)
	if err != nil {
		return gateContractSpec{}, fmt.Errorf("gate contract %s leverage_max=%q: %w", contract, spec.LeverageMax, err)
	}
	mktMax, err := f64z(spec.MarketOrderSizeMax)
	if err != nil {
		return gateContractSpec{}, fmt.Errorf("gate contract %s market_order_size_max=%q: %w", contract, spec.MarketOrderSizeMax, err)
	}
	s := gateContractSpec{
		Quanto: q, SizeMin: spec.OrderSizeMin, SizeMax: spec.OrderSizeMax,
		EnableDecimal: spec.EnableDecimal, InDelisting: spec.InDelisting, LeverageMax: lvMax,
		MarketOrderSizeMax: mktMax, Status: spec.Status,
		at: time.Now(),
	}
	gateSpecCache.Store(contract, s)
	gateQuantoCache.Store(contract, q)
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
	// D-570 包A S1c status 闸：非 trading 全拦（prelaunch/delisting/delisted/circuit_breaker）；
	// in_delisting 保留双保险。
	if spec.Status != "" && spec.Status != "trading" {
		return 0, spec, fmt.Errorf("gate %s 合约非可交易态 status=%q fail-closed 拒单", contract, spec.Status)
	}
	if spec.InDelisting {
		return 0, spec, fmt.Errorf("gate %s 合约退市中（in_delisting）fail-closed 拒单", contract)
	}
	size := qty / spec.Quanto
	if spec.SizeMin > 0 && size < spec.SizeMin {
		return 0, spec, fmt.Errorf("gate %s 不足最小张数：%.4f < order_size_min %.4f fail-closed", contract, size, spec.SizeMin)
	}
	// D-570 包A S1c 量上限合并卡：order_size_max 与 market_order_size_max 两正值取小
	// （市价单语义更严；order_size_max 缺席时 market_order_size_max 独担）。
	effMax := spec.SizeMax
	if spec.MarketOrderSizeMax > 0 && (effMax <= 0 || spec.MarketOrderSizeMax < effMax) {
		effMax = spec.MarketOrderSizeMax
	}
	if effMax > 0 && size > effMax {
		return 0, spec, fmt.Errorf("gate %s 超最大张数：%.4f > effMax %.4f（order_size_max %.4f / market_order_size_max %.4f）fail-closed",
			contract, size, effMax, spec.SizeMax, spec.MarketOrderSizeMax)
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
	// 缓存命中（包级 sync.Map，D-570 包C S6①）
	if v, ok := gateQuantoCache.Load(contract); ok {
		if q, ok2 := v.(float64); ok2 {
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
	ts := gateNow()
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
	// 写缓存（包级 sync.Map）
	gateQuantoCache.Store(contract, q)
	s := int64(math.Round(qty / q))
	if s < 1 {
		return 0, 0, fmt.Errorf("qty %.6f / quanto %.6f = %.0f < 1 张（gate 最小 1 张）", qty, q, math.Round(qty/q))
	}
	return s, q, nil
}
