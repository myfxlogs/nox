package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"arb/internal/httpx"
	"arb/internal/keydomain"
)

// CEXExecutor 是 Binance/OKX REST 签名下单执行器（§13.2）。
// 每个市场一个实例（spot/perp 是不同实例）。D-059：补 OKX 下单（OK-ACCESS-* 四头 +
// base64 HMAC），venue 分派。
// D-108 P2 T1：Binance perp 双仓探测缓存（dualSide/dualSideProbed）+ AlertFn 平仓被拒告警。
type CEXExecutor struct {
	venue     string
	venueType string // spot / perp
	// D-473 S2：spot 现金预检 taker 缓冲（SetSpotTakerBps 注入，缺省 0=不缓冲）。
	spotTakerBps float64
	apiKey       string
	apiSecret    string
	passphrase   string // OKX 必填；Binance 空
	baseURL      string // binance: proxy 根；okx: www.okx.com
	simulated    bool   // D-516：demo 域收编后纯标记位——构造期 fail-closed 闸用，不再改请求
	keyOverride  string // D-362 S3：撞键非赢家注册键（"venue:type#<id>"；空=canonical）
	client       *http.Client

	// D-108 P2 T1：Binance perp 双仓模式探测缓存（per executor 实例，避免每单探测）。
	dualSide       bool // true=hedge mode（双仓），false=one-way（单仓）
	dualSideProbed bool // 是否已探测（探测失败时 false + 保守按 hedge 处理）

	// D-120：gate futures 合约规格缓存（contract → quanto_multiplier，避免每单查）。
	// D-564 S4 U6：扩为全规格缓存（order_size_min/max、enable_decimal、in_delisting、
	// leverage_max——规格=慢变元数据允许缓存，非决策时变数据）。
	gateQuanto map[string]float64
	gateSpec   map[string]gateContractSpec
	dualSideMu sync.Mutex // 保护 dualSide/dualSideProbed 探测
	// D-125：okx perp 合约面值缓存已转包级共享 okxSwapSpecCache（D-311 R4，instId → ctVal/lotSz）。
	// okx swap sz 单位=张数，o.Qty 是基础币数量 → sz=qty/ctVal。
	// AlertFn 平仓被拒时 critical 告警（main 注入 store.InsertAlert；nil=不告警，测试用）。
	AlertFn          func(posID *int64, level, msg string)
	marginReservePct float64 // D-415 ARB_MARGIN_RESERVE_PCT 安全垫（缺省 0.20）

	// D-472 S4：强平距离预检参数（SetLiqGuards 注入；缺省 0=关闸/0.5=cross 占比上限）。
	minLiqDistPct          float64
	crossNotionalEquityCap float64
	// D-472 S4：binance tier-1 MMR 运行时缓存（leverageBracket 取不到=fail-closed）。
	mmMu    sync.Mutex
	mmCache map[string]float64
}

// defaultMarginReservePct D-415 §1.4：可用额安全垫缺省——只吃满 80%，留 20%
// 缓冲防「开完即贴爆仓线」。cfg.MarginReservePct 经 SetMarginReservePct 覆盖。
const defaultMarginReservePct = 0.20

// D-472 S4 缺省档：cross 名义/权益占比上限。minLiqDistPct 零值=关闸（struct 零值即缺省）。
// D-563 S1：gate 杠杆无缺省——缺失/非正=fail-closed 拒开（phantom 杠杆静默兜底=违规）。
const DefaultCrossNotionalEquityCap = 0.5 // D-472 S4：ARB_CROSS_NOTIONAL_EQUITY_CAP 缺省档

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

		marginReservePct:       defaultMarginReservePct,
		crossNotionalEquityCap: DefaultCrossNotionalEquityCap,
	}
}

// alertCritical 平仓被拒时 critical 告警（D-108 P2 T1，posID>0 才告警）。
func (e *CEXExecutor) alertCritical(o Order, msg string) {
	if e.AlertFn != nil && o.PosID > 0 {
		pid := o.PosID
		e.AlertFn(&pid, "critical", msg)
	}
}

// Key 注册键。D-362 S3：撞键非赢家经 SetKeyOverride 返回 "venue:type#<id>"
// （Registry.Register 以本键路由；被顶掉的真实盘执行器休眠注册不可达=D-158 安全语义保持）。
func (e *CEXExecutor) Key() string {
	if e.keyOverride != "" {
		return e.keyOverride
	}
	return keydomain.Join(e.venue, e.venueType)
}

// SetKeyOverride D-362 S3：设置撞键归一后的注册键（由 reconcile.CexVenueKey 计算）。
func (e *CEXExecutor) SetKeyOverride(k string) { e.keyOverride = k }

// Precheck 下单前预检（D-121）：gate perp 查合约存在性（testnet 部分合约缺失如 TRX_USDT，
// 不预检则一腿成交一腿失败→LEGGED）。OKX/binance perp 暂无预检（testnet 主流币种覆盖全）。
// spot 无预检需求。预检失败 → pipeline 不建 position 不下单。
func (e *CEXExecutor) Precheck(ctx context.Context, o Order) error {
	if e.venue == "gate" && e.venueType == "perp" {
		// 复用 gateContractSize 查合约存在性（qty 用 1 避免最小张数误判，只验存在）
		if _, _, err := e.gateContractSize(ctx, o.Symbol, 1); err != nil {
			return fmt.Errorf("gate precheck %s: %w", o.Symbol, err)
		}
	}
	return nil
}

func (e *CEXExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	switch e.venue {
	case "binance":
		return e.placeBinance(ctx, o)
	case "okx":
		return e.placeOKX(ctx, o)
	case "gate":
		return e.placeGate(ctx, o)
	default:
		return Fill{}, fmt.Errorf("cex executor: unsupported venue %q", e.venue)
	}
}

// placeOKX 下单：POST /api/v5/trade/order（OK-ACCESS-* 四头，SIGN=base64(HMAC-SHA256(secret,
// ts+method+path+body))）。spot tdMode=cash；perp tdMode=cross（代码保留，本次不验证）。
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
		// D-564 S6 U8/U9：posMode 判定时刻实读（非 long_short_mode 拒）+instruments.state 闸。
		if err := e.okxPosModeCheck(ctx); err != nil {
			return Fill{}, err
		}
		if err := e.okxStateCheck(ctx, instID); err != nil {
			return Fill{}, err
		}
	}
	var sz string // perp/spot 两分支各自赋值（D-516：初值 dead write 清 staticcheck）
	var bodyStr string
	var ctVal float64 // D-125：okx perp 合约面值（perp 分支赋值，Fill 处用）
	if e.venueType == "perp" {
		// D-125：okx swap sz 单位=张数，o.Qty 是基础币数量 → sz=qty/ctVal。
		// 如 TRX-USDT-SWAP ctVal=1000 → 2964 TRX = 2.964 张。不换算则 2964 张=2964000 TRX，
		// 名义价值~$100万，保证金需求远超账户余额 → 51008 Insufficient margin 拒单 → LEGGED。
		var lotSz float64
		var err error
		ctVal, lotSz, err = e.okxContractVal(ctx, instID)
		if err != nil {
			return Fill{}, fmt.Errorf("okx contract spec: %w", err)
		}
		szContracts := o.Qty / ctVal
		// sz 必须是 lotSz 的倍数（okx 51121 Order quantity must be a multiple of the lot size）。
		// 向下取整到 lotSz 倍数（不开过量仓位），如 2.964/0.01=296.4 → 296×0.01=2.96 张。
		lots := math.Floor(szContracts / lotSz)
		szContracts = lots * lotSz
		if szContracts < lotSz { // 最小 1 个 lot
			return Fill{}, fmt.Errorf("okx qty %.6f / ctVal %.6f = %.6f 张 < lotSz %.6f（最小张数）", o.Qty, ctVal, o.Qty/ctVal, lotSz)
		}
		sz = strconv.FormatFloat(szContracts, 'f', -1, 64)
		// swap/futures 必须带 posSide（D-059 实施修正② 实测 51000）。本账户 posMode=long_short_mode
		// （account/config 实测）。开仓传空 → buy=long / sell=short 派生；平仓由调用方显式传
		// PositionSide=原持仓侧（BUY 开 long → SELL 平 + posSide=long 才减 long，D-059 实测
		// SELL+posSide=short 会新开空仓）。net_mode 账户需改 posSide=net。
		// D-108 P2 T1：平仓补 reduceOnly=true（防开反向敞口）。
		// D-120 修复：开仓（PositionSide=""）不带 reduceOnly——开仓带 reduceOnly 会被拒
		// （Binance -2022 ReduceOnly rejected / OKX 资金计算异常 Insufficient margin）。
		posSide := o.PositionSide
		if posSide == "" {
			posSide = "long"
			if side == "sell" {
				posSide = "short"
			}
		}
		isClose := o.PositionSide != ""
		// D-195-B §4.3：client_order_id 透传（OKX 支持 clOrdId，32 字符限制）。
		clOrdID := ""
		if o.ClientOrderID != "" {
			clOrdID = fmt.Sprintf(`,"clOrdId":%q`, o.ClientOrderID)
		}
		bodyStr = fmt.Sprintf(`{"instId":%q,"tdMode":%q,"side":%q,"ordType":"market","sz":%q,"posSide":%q,"reduceOnly":%t%s}`,
			instID, tdMode, side, sz, posSide, isClose, clOrdID)
	} else {
		// spot 市价单：缺省买方向 sz=报价币金额（0.0001 当 0.0001 USDT → 51020 最小金额，实测）。
		// 显式 tgtCcy=base_ccy 固定 sz=基础币数量，与 exec.Order.Qty 契约一致。
		// D-473（KB okx-perp-ctval-qty-conversion 同型）：spot sz 同样须 lotSz 倍数
		// 且 ≥minSz——取不到/不足 → fail-closed 拒单（不指望 venue 报错兜底）。
		lotSz, minSz, err := e.okxSpotLotSz(ctx, instID)
		if err != nil {
			return Fill{}, fmt.Errorf("okx spot spec: %w", err)
		}
		qty := math.Floor(o.Qty/lotSz) * lotSz
		if qty < minSz || qty < lotSz {
			return Fill{}, fmt.Errorf("okx spot qty %.6f → lotSz 地板 %.6f < minSz %.6f（最小量）", o.Qty, qty, minSz)
		}
		sz = strconv.FormatFloat(qty, 'f', -1, 64)
		// D-195-B §4.3：client_order_id 透传（OKX 支持 clOrdId）。
		clOrdID := ""
		if o.ClientOrderID != "" {
			clOrdID = fmt.Sprintf(`,"clOrdId":%q`, o.ClientOrderID)
		}
		bodyStr = fmt.Sprintf(`{"instId":%q,"tdMode":%q,"side":%q,"ordType":"market","sz":%q,"tgtCcy":"base_ccy"%s}`,
			instID, tdMode, side, sz, clOrdID)
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

	body, err := e.post(ctx, e.baseURL+path, bodyStr, headers)
	if err != nil {
		e.alertCritical(o, fmt.Sprintf("okx close rejected pos=%d symbol=%s side=%s: %v", o.PosID, o.Symbol, side, err))
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
		e.alertCritical(o, fmt.Sprintf("okx close rejected pos=%d code=%s: %s", o.PosID, resp.Code, detail))
		return Fill{}, fmt.Errorf("okx order: code=%s msg=%s", resp.Code, detail)
	}
	if len(resp.Data) == 0 {
		return Fill{}, fmt.Errorf("okx order: empty data")
	}
	d := resp.Data[0]
	if d.SCode != "" && d.SCode != "0" {
		return Fill{}, fmt.Errorf("okx order: sCode=%s sMsg=%s", d.SCode, d.SMsg)
	}
	qty, err := f64z(d.AccFillSz)
	if err != nil {
		return Fill{}, fmt.Errorf("okx order %s accFillSz=%q: %w", d.OrdID, d.AccFillSz, err)
	}
	avgPx, err := f64z(d.AvgPx)
	if err != nil {
		return Fill{}, fmt.Errorf("okx order %s avgPx=%q: %w", d.OrdID, d.AvgPx, err)
	}
	// D-125：okx perp accFillSz 是张数，转回基础币数量 = 张数 × ctVal。
	if e.venueType == "perp" {
		qty *= ctVal
	}
	if qty <= 0 || avgPx <= 0 {
		// 市价单 POST 响应可能先于撮合返回（accFillSz=0/avgPx 空，D-059 实施修正② 实测：
		// 订单实际成交但响应无成交数据）→ 退避轮询 ≥3s；窗尽 cancel+终查双证；
		// 仍不可判 → ErrFillUnconfirmed（D-421：受理未确认≠确定未成交，禁映 FAILED）。
		return e.okxConfirmFill(ctx, o, instID, d.OrdID, ctVal)
	}
	feeUSD := e.okxOrderFeeUSD(ctx, instID, d.OrdID) // D-391 S2：补查 fee（fail-open 0→NULL）
	return Fill{OrderID: d.OrdID, Price: avgPx, Qty: qty, FeeUSD: feeUSD, Ts: time.Now()}, nil
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
