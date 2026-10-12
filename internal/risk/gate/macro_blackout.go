// gate_macro_blackout.go —— D-053 ③：高影响财历事件执行禁区闸。
// 开仓侧 Gate.Submit（incidentGate 后）：窗（±ARB_MACRO_BLACKOUT_MIN，缺省 15）内存在
// importance='high' 事件且 currency ∈ 禁区币种集 → REJECTED + rejectLog（reason 固定
// 前缀 "macro blackout"——selfwatch 禁区拒开计数依赖该前缀 LIKE）。查询失败 fail-closed 拒开。
// 平仓侧 evaluatePosition（allLegsActionable 后、checkExitCondition 前）同窗判定 →
// markLegSuspend(macro_blackout) 推迟本轮常规退出；查询失败 fail-open（不推迟=照常评估）；
// disasterCheckXAsset/止损路径不经此闸（独立调用链）。
// 币种集 = ARB_MACRO_BLACKOUT_CURRENCIES（热读，缺省 USD，逗号分隔）∪ canonical 为
// 6 字母且两半均为 ISO 币种码时的两币。配置键热读（app_config 直读回落 cfg，incidentGate 先例）。
package gate

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/data/macro"
)

// macroBJLoc 禁区 reason 的 BJ 时刻坐标（macro 包 bjLoc 同值独立定义——包间不互引）。
var macroBJLoc = func() *time.Location {
	l, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return l
}()

// macroBlackoutCurrencies 禁区币种集推导：热读配置基集 ∪ canonical 两半（6 字母且均为 ISO）。
// 读取失败回落 cfg（boot env 值）；基集空串=缺省 USD。
func (g *Gate) macroBlackoutCurrencies(ctx context.Context, canonical string) []string {
	base := g.cfg.MacroBlackoutCurrencies
	if v, ok, err := g.store.AppConfigString(ctx, "ARB_MACRO_BLACKOUT_CURRENCIES"); err != nil {
		log.Printf("[autotrade] macro blackout currencies read: %v（回落 cfg）", err)
	} else if ok && strings.TrimSpace(v) != "" {
		base = v
	}
	return macro.BlackoutCurrencySet(base, canonical)
}

// blackoutCurrencySet 禁区币种集纯函数（D-053 R1④ Gate/Engine 共用件）：配置基集
// （逗号分隔，空=USD 缺省）∪ canonical 6 字母两半 ISO 推导（两半皆 ISO 才并入——
// EURUSD/XAUUSD 命中；BTCUSDT 7 字母、SOLUSDT 非 ISO 半不推导）。ANY 匹配序不敏感。
// macroBlackoutWindow 禁区窗分钟数（热读；缺省回落 cfg；<=0=禁用返回 false）。
func (g *Gate) macroBlackoutWindow(ctx context.Context) (int, bool) {
	minutes := g.cfg.MacroBlackoutMin
	if v, ok, err := g.store.AppConfigInt64(ctx, "ARB_MACRO_BLACKOUT_MIN"); err != nil {
		log.Printf("[autotrade] macro blackout min read: %v（回落 cfg）", err)
	} else if ok {
		minutes = int(v)
	}
	if minutes <= 0 {
		return 0, false
	}
	return minutes, true
}

// macroBlackoutGate 开仓闸。返回 (result,true)=拒开。fail-closed：查询失败=拒。
func (g *Gate) macroBlackoutGate(ctx context.Context, intent *TradeIntent) (GateResult, bool) {
	minutes, on := g.macroBlackoutWindow(ctx)
	if !on {
		return GateResult{}, false
	}
	now := time.Now()
	events, err := g.store.MacroHighEventsIn(ctx,
		now.Add(-time.Duration(minutes)*time.Minute), now.Add(time.Duration(minutes)*time.Minute),
		g.macroBlackoutCurrencies(ctx, intent.Canonical))
	if err != nil {
		g.rejectLog(ctx, *intent, fmt.Sprintf("macro blackout check: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "macro blackout check failed"}, true
	}
	if len(events) == 0 {
		return GateResult{}, false
	}
	e := events[0] // 最早事件入 reason（同窗多事件下轮/平后自见）
	reason := fmt.Sprintf("macro blackout %s %s（BJ %s ±%dmin）",
		e.Currency, e.Name, e.EventTime.In(macroBJLoc).Format("01-02 15:04"), minutes)
	g.rejectLog(ctx, *intent, reason, false)
	return GateResult{Status: "REJECTED", Reason: reason}, true
}
