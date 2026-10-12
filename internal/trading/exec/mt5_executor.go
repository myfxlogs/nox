package exec

import (
	"arb/internal/data/keydomain"
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	mt5pb "arb/internal/data/adapter/gen/mtapi/mt5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// MT5Executor 复用 MT5Adapter 的 gRPC 连接执行交易（§13.2）。
// 与 MT4 严格隔离——独立 proto 类型、独立类。
type MT5Executor struct {
	venue    string
	trading  mt5pb.TradingClient
	mt5      mt5pb.MT5Client // D-415：RequiredMargin/AccountSummary 资金预检面
	token    string
	venueKey string // "venue:mt5"
	// marginReservePct 可用保证金安全垫（D-415 ARB_MARGIN_RESERVE_PCT，缺省 0.20）。
	marginReservePct float64
}

// NewMT5Executor 创建 MT5 执行器。mt5=MT5Client 服务面（MarginPrecheck 用）。
func NewMT5Executor(venue, token string, trading mt5pb.TradingClient, mt5 mt5pb.MT5Client) *MT5Executor {
	return &MT5Executor{
		venue:            venue,
		trading:          trading,
		mt5:              mt5,
		token:            token,
		venueKey:         keydomain.Join(venue, "mt5"),
		marginReservePct: defaultMarginReservePct,
	}
}

// SetMarginReservePct 注入资金预检安全垫（cfg.MarginReservePct；不设=缺省 0.20）。
func (e *MT5Executor) SetMarginReservePct(pct float64) { e.marginReservePct = pct }

// pessimisticLeverage D-006 S1：悲观杠杆档 SSOT 常量（业主拍板 20，不进 env）——
// Exness 无限杠杆快照（实拍 lev=2e9 → marginRate≈0，预检形同虚设）按政策收紧
// 情景预留：effLev=min(实测,20)，required 下界=名义/20。
const pessimisticLeverage = 20.0

// effectiveLeverage 悲观档折减：实测更低按实测，高/无限按悲观档。
func effectiveLeverage(lev float64) float64 {
	if lev <= 0 || lev > pessimisticLeverage {
		return pessimisticLeverage
	}
	return lev
}

func (e *MT5Executor) Key() string { return e.venueKey }

// MarginPrecheck D-415 §1.2：MT5 venue 原生资金校验——RequiredMargin(symbol,lots,
// 市价 price=0) vs AccountSummary.FreeMargin×(1−reservePct)。required 超限或任一
// RPC 失败 → 返 err 拒开（fail-closed：证明不了能开就不开）。
func (e *MT5Executor) MarginPrecheck(ctx context.Context, o Order) error {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", e.token)
	dealType := mt5pb.DealType_DealType_DealBuy
	if o.Side == "SELL" {
		dealType = mt5pb.DealType_DealType_DealSell
	}
	rm, err := e.mt5.RequiredMargin(ctx, &mt5pb.RequiredMarginRequest{
		Id: e.token, Symbol: o.Symbol, Lots: o.Qty, Type: &dealType,
	})
	if status.Code(err) == codes.Unimplemented {
		// RequiredMargin 全节点缺席（能力缺席≠瞬态失败）→ 降级 venue 已服数据估算。
		return e.marginPrecheckEstimate(ctx, o)
	}
	if err != nil {
		return fmt.Errorf("mt5 required margin: %w", err)
	}
	if err := rm.GetError(); err != nil {
		return fmt.Errorf("mt5 required margin: [code=%s] %s", err.GetCode().String(), err.GetMessage())
	}
	// 悲观档双保守取大——broker 报告值 vs 名义/悲观杠杆（无限杠杆 broker 报 ≈0
	// 时以名义/20 兜底）。est 取数失败 fail-closed（证明不了名义就不放行）。
	cs, px, lev, fm, err := e.marginSymbolInputs(ctx, o)
	if err != nil {
		return err
	}
	required := math.Max(rm.GetResult(), o.Qty*cs*px/effectiveLeverage(lev))
	avail := fm * (1 - e.marginReservePct)
	if required > avail {
		return fmt.Errorf("mt5 margin insufficient: required %.2f > avail %.2f (broker %.2f effLev %.0f freeMargin %.2f × %.2f)",
			required, avail, rm.GetResult(), effectiveLeverage(lev), fm, 1-e.marginReservePct)
	}
	return nil
}

// marginSymbolInputs D-006 S1：主路径/估算路径共用取数——SymbolParams（cs+marginCcy
// 闸）+ GetQuote（方向价）+ AccountSummary（lev+freeMargin）。任一步失败 fail-closed。
func (e *MT5Executor) marginSymbolInputs(ctx context.Context, o Order) (cs, px, lev, fm float64, err error) {
	sp, err := e.mt5.SymbolParams(ctx, &mt5pb.SymbolParamsRequest{Id: e.token, Symbol: o.Symbol})
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("margin estimate symbol params: %w", err)
	}
	if err := sp.GetError(); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("margin estimate symbol params: [code=%s] %s", err.GetCode().String(), err.GetMessage())
	}
	info := sp.GetResult().GetSymbolInfo()
	if info == nil {
		return 0, 0, 0, 0, fmt.Errorf("margin estimate symbol params: nil SymbolInfo for %s", o.Symbol)
	}
	cs, mc := info.GetContractSize(), info.GetMarginCurrency()
	if cs <= 0 {
		return 0, 0, 0, 0, fmt.Errorf("margin estimate: contract size %.4g for %s", cs, o.Symbol)
	}
	if mc != "USD" && !(strings.HasPrefix(o.Symbol, mc) && strings.Contains(o.Symbol, "USD")) {
		return 0, 0, 0, 0, fmt.Errorf("margin estimate: unsupported marginCcy %s for %s", mc, o.Symbol)
	}
	q, err := e.mt5.GetQuote(ctx, &mt5pb.GetQuoteRequest{Id: e.token, Symbol: o.Symbol})
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("margin estimate quote: %w", err)
	}
	if err := q.GetError(); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("margin estimate quote: [code=%s] %s", err.GetCode().String(), err.GetMessage())
	}
	px = q.GetResult().GetAsk()
	if o.Side == "SELL" {
		px = q.GetResult().GetBid()
	}
	if px <= 0 {
		return 0, 0, 0, 0, fmt.Errorf("margin estimate: quote px %.4g for %s", px, o.Symbol)
	}
	as, err := e.mt5.AccountSummary(ctx, &mt5pb.AccountSummaryRequest{Id: e.token})
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("margin estimate account summary: %w", err)
	}
	if err := as.GetError(); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("margin estimate account summary: [code=%s] %s", err.GetCode().String(), err.GetMessage())
	}
	lev = as.GetResult().GetLeverage()
	if lev <= 0 {
		return 0, 0, 0, 0, fmt.Errorf("margin estimate: leverage %.4g", lev)
	}
	return cs, px, lev, as.GetResult().GetFreeMargin(), nil
}

func (e *MT5Executor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", e.token)
	op := mt5pb.OrderType_OrderType_Buy
	if o.Side == "SELL" {
		op = mt5pb.OrderType_OrderType_Sell
	}
	// MT5 Comment 是 oneof 指针字段，取本地副本地址传入。
	// 与 MT4 统一携带 OrderComment 标记（D-032：对账判别 ARB 源订单）。
	comment := OrderComment
	req := &mt5pb.OrderSendRequest{
		Id:        e.token,
		Symbol:    o.Symbol,
		Operation: op,
		Volume:    o.Qty,
		Comment:   &comment,
	}
	// 灾难 SL 原子写入（Stoploss 是 oneof *float64，非零才挂）。
	if o.StopLoss > 0 {
		sl := o.StopLoss
		req.Stoploss = &sl
	}
	resp, err := e.trading.OrderSend(ctx, req)
	if err != nil {
		return Fill{}, fmt.Errorf("mt5 ordersend: %w", err)
	}
	if err := resp.GetError(); err != nil {
		// 修复⑩ G3：错误码写入 message 供 errclass.Classify 解析（[code=XXX] 前缀）。
		// TRADE_RETCODE_MARKET_CLOSED(10018)——干净 FAILED（非 UNKNOWN/不进 LEGGED）：
		// executeLeg 判定链=非 timeout 即 FAILED ✓；此处显式类型化防未来 UNKNOWN 化，message
		// 禁携带 "deadline/timeout" 字样（isContextTimeoutErr 字符串误判面）。
		if msg := err.GetMessage(); err.GetCode().String() == "10018" ||
			strings.Contains(strings.ToLower(msg), "market closed") {
			return Fill{}, fmt.Errorf("mt5 ordersend: market closed (10018): %s", msg)
		}
		return Fill{}, fmt.Errorf("mt5 ordersend: [code=%s] %s", err.GetCode().String(), err.GetMessage())
	}
	ord := resp.GetResult()
	if ord == nil {
		return Fill{}, fmt.Errorf("mt5 ordersend: nil result")
	}
	// overfill 兜底告警——MT 服务端规整 qty 可能大于请求（posID=470 实证 0.0109→0.02）。
	// 单已成交不可撤，仅告警留痕（digest 可捕捉）。
	if filled := ord.GetLots(); filled > o.Qty*1.001 {
		log.Printf("[exec] ERROR overfill: requested %.4f filled %.4f symbol=%s venue=%s",
			o.Qty, filled, o.Symbol, e.venueKey)
	}
	// 订单响应通常无 commission——OrderHistory 回查 deal 级费（fail-open 0）。
	comm := ord.GetCommission()
	if hc := e.orderHistoryCommission(ctx, ord.GetTicket(), false); hc != 0 {
		comm = hc
	}
	return Fill{
		OrderID:    strconv.FormatInt(ord.GetTicket(), 10),
		Price:      ord.GetOpenPrice(),
		Qty:        ord.GetLots(),
		Swap:       0,
		Commission: comm,
		Ts:         time.Now(),
	}, nil
}

func (e *MT5Executor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", e.token)
	ticket, err := strconv.ParseInt(ref.OrderID, 10, 64)
	if err != nil {
		return Fill{}, fmt.Errorf("mt5 close: bad ticket %q: %w", ref.OrderID, err)
	}
	resp, err := e.trading.OrderClose(ctx, &mt5pb.OrderCloseRequest{
		Id:     e.token,
		Ticket: ticket,
	})
	if err != nil {
		return Fill{}, fmt.Errorf("mt5 orderclose: %w", err)
	}
	if err := resp.GetError(); err != nil {
		return Fill{}, fmt.Errorf("mt5 orderclose: [code=%s] %s", err.GetCode().String(), err.GetMessage())
	}
	ord := resp.GetResult()
	if ord == nil {
		return Fill{}, fmt.Errorf("mt5 orderclose: nil result")
	}
	// OrderClose 返回 Order 是平仓后状态：Lots=剩余持仓（全平=0），CloseLots=本次平仓手数。
	// 优先 CloseLots；旧 gateway 不填 CloseLots 时 fallback 到 Lots（兼容）。
	qty := ord.GetCloseLots()
	if qty <= 0 {
		qty = ord.GetLots()
	}
	// 平仓腿佣金走 OrderHistory out-deal 回查（响应字段通常空）；
	// out 腿只计 out/partial-close deal，不重复入场 in-deal。fail-open 0→NULL。
	comm := ord.GetCommission()
	if hc := e.orderHistoryCommission(ctx, ticket, true); hc != 0 {
		comm = hc
	}
	return Fill{
		OrderID:    ref.OrderID,
		Price:      ord.GetClosePrice(),
		Qty:        qty,
		Swap:       ord.GetSwap(),
		Commission: comm,
		Ts:         time.Now(),
	}, nil
}

// marginPrecheckEstimate D-428 §1.2：RequiredMargin Unimplemented 时的降级估算——
// requiredUSD = lots × ContractSize × price / effLev，avail = FreeMargin×(1−reservePct)。
// 证据源全部 venue 已服（marginSymbolInputs 共用取数）；任一步失败 fail-closed。
// lev 不再退化——effLev=min(lev,20) 悲观档，无限杠杆哨兵（Exness 实拍 2e9）
// → 名义/20（政策收紧情景预留，不退化为 FreeMargin 底线）。
func (e *MT5Executor) marginPrecheckEstimate(ctx context.Context, o Order) error {
	cs, px, lev, fm, err := e.marginSymbolInputs(ctx, o)
	if err != nil {
		return err
	}
	effLev := effectiveLeverage(lev)
	requiredUSD := o.Qty * cs * px / effLev
	avail := fm * (1 - e.marginReservePct)
	if requiredUSD > avail {
		return fmt.Errorf("mt5 margin insufficient (estimate): required %.2f > avail %.2f (lots %.4g cs %.4g px %.4g lev %.0f effLev %.0f freeMargin %.2f × %.2f)",
			requiredUSD, avail, o.Qty, cs, px, lev, effLev, fm, 1-e.marginReservePct)
	}
	log.Printf("[exec] mt5 margin estimate pass: %s %s qty=%.4g required=%.2f avail=%.2f (cs=%.4g px=%.4g lev=%.0f effLev=%.0f)",
		o.Side, o.Symbol, o.Qty, requiredUSD, avail, cs, px, lev, effLev)
	return nil
}
