package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
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
	// 杠杆纯读化——开仓前 SET 段退役（push 端点带切保证金模式副作用永久禁用）；
	// precheck 经 get_leverage 实读 venue 实况，读不到 fail-closed 拒开。
	// 平仓（PositionSide!=""）——D-564 S3 U4 dual 模式分叉：dual/dual_plus 官方
	// 机制=auto_size=close_long|close_short；D-576 服务端错误原文校正契约：auto_size 出场
	// 必配四约束 size=0+reduce_only=true+close=false（旧注释「reduce_only 在 dual 语义未定义」
	// 被生产 400 拒单证伪——它是必备件，close:true 才是单仓残留违约字段）；single 维持
	// reduce_only=true。模式每次实读 accounts（禁缓存——
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
		// gate futures size 单位=合约数，o.Qty 是基础币数量 → 查合约规格换算。
		// gateSpecCheck 承接换算+前置闸（in_delisting/size min/max/整数张 floor）。
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
		// text 单号真实化（t-<clientOrderID> 截28B；空=t-nox 兜底）。
		// close=false 显式写出（服务端契约锚：auto_size 须 size=0&reduce_only=true&
		// close=false——2026-09-29 生产 400 INVALID_ARGUMENT 实证，省略亦为 false 但显式可读）。
		bodyStr = fmt.Sprintf(`{"contract":%q,"size":0,"price":"0","tif":"ioc","text":%q,"close":false,"auto_size":%q,"reduce_only":true}`,
			o.Symbol, gateOrderText(o), closeSide)
	} else {
		// size 序列化按 enable_decimal 分支——false=整数张 %d（现形）；
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
	// 签名+发送入 resilience——REQUEST_EXPIRED → 校时后同 bodyStr/urlPath 重签重发一次。
	rawBody, err := e.gateSignedPost(ctx, endpoint, urlPath, bodyStr, true)
	if err != nil {
		// S6②：拒单驱动失效——规格类标签清本 contract 缓存（本单不重试按现状上抛）。
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
	// gateFilled 返回的 qty 是合约数 → 乘 quanto 回填为基础币数量（Fill.Qty 契约=基础币）。
	toBaseQty := func(fill Fill) Fill { fill.Qty = fill.Qty * quanto; return fill }
	if ord.Status == "open" {
		// S4②：内联 5×100ms 轮询重构为 gateConfirmFill 三分量窗
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
	// POST 响应无 fee 字段——best-effort 查订单事件快照补 fee
	//（WS usertrades 帧可能晚到；未命中=honest 0→NULL 不阻断成交）。
	if e.gateOrderLookup != nil {
		if sn, ok := e.gateOrderLookup(strconv.FormatInt(ord.ID, 10)); ok && sn.FeeUSD > 0 {
			fill.FeeUSD = sn.FeeUSD
		}
	}
	return rawBody, toBaseQty(fill), nil
}
