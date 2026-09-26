package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// SetMarginReservePct 注入资金预检安全垫（cfg.MarginReservePct；不设=缺省 0.20）。
func (e *CEXExecutor) SetMarginReservePct(pct float64) { e.marginReservePct = pct }

// SetLiqGuards D-472 S4：注入强平距离预检参数（cfg 注入）。
// minLiqDistPct=isolated 距离下限（ARB_MIN_LIQ_DISTANCE_PCT，缺省 0=显式关闸——
// 开启前置=回放定标报告，复审方标定后才允许非零值上线）；crossNotionalEquityCap=
// cross 名义/权益占比上限（ARB_CROSS_NOTIONAL_EQUITY_CAP，缺省 0.5；≤0=cross 支路关）。
func (e *CEXExecutor) SetLiqGuards(minLiqDistPct, crossNotionalEquityCap float64) {
	e.minLiqDistPct = minLiqDistPct
	e.crossNotionalEquityCap = crossNotionalEquityCap
}

// SetGateDefaultLeverage D-472 S2：注入 gate venue 级缺省杠杆档
// （ARB_GATE_DEFAULT_LEVERAGE，缺省 5——账户级杠杆取不到时的第二级，不得 error）。
func (e *CEXExecutor) SetGateDefaultLeverage(lev float64) { e.gateDefaultLeverage = lev }

// MarginPrecheck D-415 §1.3 + D-472 S1/S2/S4：开仓前逐腿资金预检。
// 量纲锚（B-001 钉死）：exec.Order.Qty=基础币数量（pipeline orderQtyFor ×cs 换算后），
// requiredIM=o.Qty×mark/leverage——禁二次乘 ct_val/quanto（binance ct_val≡1、gate quanto
// 已含在 price 定义内、okx 现路径 max-size 服务端已算）。
// 范围：okx/binance/gate 的 perp 分支补实；其余 venue/type 返 nil（MT5 有独立实现）。
// 任一调用失败 → 返 err 拒开（fail-closed，同 okx 先例）。
//
// D-472 S4 MMR/杠杆实测定标口径（禁「保守值」模糊表达，B-006）：
//   - okx XPT/XPD tier-1（cross）：mmr=1.0%、imr=2.0%、maxLever=50
//     （来源=/api/v5/public/position-tiers?instType=SWAP&uly=XPT-USDT&tdMode=cross，2026-09-25 实拉；
//     okx 下单 tdMode 硬编码 cross → 恒走占比闸，该常量仅文档用不参与判定）；
//   - binance：isolated 分支 MMR 运行时取 /fapi/v1/leverageBracket（签名，per-symbol 缓存），
//     取不到=error 不猜——2026-09-25 生产密钥实测 -2015 Invalid API-key/IP/permissions
//     （证据在案），禁硬编码防保守值取错方向=静默失效；
//   - gate：全账户 cross 强平模型（contract payload 实测无 mmr 字段，2026-09-25），
//     本票 gate 恒走占比闸，MMR 不适用。
func (e *CEXExecutor) MarginPrecheck(ctx context.Context, o Order) error {
	switch {
	case e.venue == "okx" && e.venueType == "perp":
		if err := e.okxMarginPrecheck(ctx, o); err != nil {
			return err
		}
		// okx tdMode 硬编码 cross（cex_executor.go placeOkx）→ S4 占比闸。
		// 关闸（缺省）=零额外调用（mark/balance 两请求仅在闸开启时发生）。
		if e.minLiqDistPct <= 0 {
			return nil
		}
		mark, eq, err := e.okxMarkAndEquity(ctx, o.Symbol)
		if err != nil {
			return fmt.Errorf("okx liq guard %s: %w", o.Symbol, err)
		}
		return e.liqDistanceGuard(ctx, o, liqInput{venue: "okx", mark: mark, equity: eq})
	case e.venue == "binance" && e.venueType == "perp":
		return e.binanceMarginPrecheck(ctx, o)
	case e.venue == "gate" && e.venueType == "perp":
		return e.gateMarginPrecheck(ctx, o)
	case e.venueType == "spot":
		// D-473 S2：现货腿全额现金占用预检（carry 场景 BUY 腿）；SELL 腿内部放行。
		return e.spotCashPrecheck(ctx, o)
	}
	return nil
}

// okxMarginPrecheck D-415 原路径（提取自 MarginPrecheck，零语义变更）：
// GET /api/v5/account/max-size?instId&tdMode=cross 服务端已含余额+杠杆+持仓，
// szContracts ≤ avail×(1−reservePct) 才放行。
type liqInput struct {
	venue    string
	isolated bool    // binance positions[].isolated 实值；okx/gate 恒 cross
	leverage float64 // isolated 分支用（binance 腿级实值）
	mark     float64
	equity   float64 // cross 占比闸分母（binance totalMarginBalance / gate total / okx totalEq）
}

// liqDistanceGuard D-472 S4：强平距离预检（现闸只答「能不能开」，本闸答「开完离爆多远」）。
// 缺省 ARB_MIN_LIQ_DISTANCE_PCT=0=显式关闸（开启前置=5 组合回放定标报告，复审方标定后
// 才允许非零值上线）。模式分流（B-006）：
//   - isolated：estLiqDist=(1/leverage_eff)−mmr_est ≥ minPct；MMR 运行时取 leverageBracket
//     （fail-closed，不硬编码——binance -2015 实测在案）；
//   - cross：禁按腿杠杆估（语义=全账扛，按腿算=系统性高估恒放行）→ 名义/权益占比闸
//     notional/equity ≤ CrossNotionalEquityCap；equity 非正=fail-closed。
//
// 拒文含 liq-distance 子串 → gate 层 S3 节流告警同规则捕获。
func (e *CEXExecutor) liqDistanceGuard(ctx context.Context, o Order, in liqInput) error {
	if e.minLiqDistPct <= 0 {
		return nil // 显式关闸（缺省）
	}
	notional := o.Qty * in.mark
	if in.isolated {
		mmr, err := e.binanceMMRTier1(ctx, o.Symbol)
		if err != nil {
			return fmt.Errorf("%s liq guard %s: %w", in.venue, o.Symbol, err)
		}
		est := 1/in.leverage - mmr
		if est < e.minLiqDistPct {
			return fmt.Errorf("%s liq-distance est=%.4f < floor=%.4f (isolated lev=%.0f mmr=%.4f notional=%.2f)",
				in.venue, est, e.minLiqDistPct, in.leverage, mmr, notional)
		}
		return nil
	}
	if e.crossNotionalEquityCap <= 0 {
		return nil // cross 支路显式关（cap≤0）
	}
	if in.equity <= 0 {
		return fmt.Errorf("%s liq guard %s: equity=%.4g 非正（cross 占比闸 fail-closed）", in.venue, o.Symbol, in.equity)
	}
	if ratio := notional / in.equity; ratio > e.crossNotionalEquityCap {
		return fmt.Errorf("%s liq-distance cross notional/equity=%.3f > cap=%.3f (notional=%.2f equity=%.2f，占比闸禁腿杠杆估)",
			in.venue, ratio, e.crossNotionalEquityCap, notional, in.equity)
	}
	return nil
}

// binanceMMRTier1 D-472 S4：binance perp tier-1 维持保证金率（signed
// /fapi/v1/leverageBracket，per-symbol 缓存）。运行时取值 fail-closed——不硬编码常量
// （2026-09-25 生产密钥实测 -2015 Invalid API-key/IP/permissions，取错方向=静默失效）。
// jsonNum JSON number/string → float64（binance 响应数值字段 string/number 混形）。
// D-508：解析失败 error 上抛——保证金预检路径脏字段不得静默归零。
func jsonNum(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case json.Number:
		return x.Float64()
	case string:
		if x == "" {
			return 0, nil // ""=字段缺席（同 f64z 语义）
		}
		return strconv.ParseFloat(x, 64)
	case nil:
		return 0, nil // 字段缺席=0（binance 可选字段缺省语义保留）
	}
	return 0, fmt.Errorf("jsonNum: unexpected type %T", v)
}
