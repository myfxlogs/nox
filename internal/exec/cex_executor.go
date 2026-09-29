package exec

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

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

	// D-566 S3：50102 校时偏移（serverTs-localMs；atomic，okxNowISO 全签名点消费）。
	clockDeltaMs atomic.Int64
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

func (e *CEXExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	// CEX 平仓 = 反向市价单
	symbol := ref.Symbol
	// 推断 side：CEX 没有"close"概念，需要外部传入方向
	// 简化：CloseOrder 在 CEX 场景由执行管线直接调 PlaceOrder 反向
	// 此方法保留接口完整性，实际 CEX 平仓走 PlaceOrder
	_ = symbol
	return Fill{}, fmt.Errorf("cex: use PlaceOrder with opposite side for close")
}
