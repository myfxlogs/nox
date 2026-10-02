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
	OrderPriceRound    float64   // D-018 阶段a：order_price_round 价格步长（post-only 限价取整；0=未采集 maker fail-closed）
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
		OrderPriceRound    string  `json:"order_price_round"` // D-018 阶段a：价格步长（post-only 限价取整）
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
	priceRound, err := f64z(spec.OrderPriceRound) // D-018 阶段a：0=未采集（maker 侧 fail-closed，不猜步长）
	if err != nil {
		return gateContractSpec{}, fmt.Errorf("gate contract %s order_price_round=%q: %w", contract, spec.OrderPriceRound, err)
	}
	s := gateContractSpec{
		Quanto: q, SizeMin: spec.OrderSizeMin, SizeMax: spec.OrderSizeMax,
		EnableDecimal: spec.EnableDecimal, InDelisting: spec.InDelisting, LeverageMax: lvMax,
		MarketOrderSizeMax: mktMax, Status: spec.Status, OrderPriceRound: priceRound,
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
