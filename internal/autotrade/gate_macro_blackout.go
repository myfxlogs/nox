// gate_macro_blackout.go —— D-053 ③：高影响财历事件执行禁区闸。
// 开仓侧 Gate.Submit（incidentGate 后）：窗（±ARB_MACRO_BLACKOUT_MIN，缺省 15）内存在
// importance='high' 事件且 currency ∈ 禁区币种集 → REJECTED + rejectLog（reason 固定
// 前缀 "macro blackout"——selfwatch 禁区拒开计数依赖该前缀 LIKE）。查询失败 fail-closed 拒开。
// 平仓侧 evaluatePosition（allLegsActionable 后、checkExitCondition 前）同窗判定 →
// markLegSuspend(macro_blackout) 推迟本轮常规退出；查询失败 fail-open（不推迟=照常评估）；
// disasterCheckXAsset/止损路径不经此闸（独立调用链）。
// 币种集 = ARB_MACRO_BLACKOUT_CURRENCIES（热读，缺省 USD，逗号分隔）∪ canonical 为
// 6 字母且两半均为 ISO 币种码时的两币。配置键热读（app_config 直读回落 cfg，incidentGate 先例）。
package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/macro"
	"arb/internal/store/pgstore"
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

// positionCanonical 持仓 canonical 解析：opportunity 行优先（XASSET_CARRY 卖腿=CanonicalSell
// ——禁区按卖腿币种判），失败回落 DedupKey 第二段（strategy:canonical:vBuy:vSell）。
func (e *Engine) positionCanonical(ctx context.Context, p *pgstore.Position) string {
	if opp, err := e.store.GetOpportunity(ctx, p.OpportunityID); err == nil && opp != nil {
		if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
			return *opp.CanonicalSell
		}
		return opp.Canonical
	}
	if parts := strings.Split(p.DedupKey, ":"); len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

// macroBlackoutActive 禁区判定共用件（D-053 R1②：suspend/xassetExitCheck 两路共用）。
// 命中 → (true, why)；未启用/未命中/查询失败 → (false, "")。
// fail-open：查询/配置失败=照常评估（平仓侧推迟不救命——真实灾难走 disaster 独立链；
// 查询失败推迟只会冻结常规退出）。
func (e *Engine) macroBlackoutActive(ctx context.Context, canonical string) (bool, string) {
	minutes := e.cfg.MacroBlackoutMin
	if v, ok, err := e.store.AppConfigInt64(ctx, "ARB_MACRO_BLACKOUT_MIN"); err != nil {
		return false, "" // fail-open
	} else if ok {
		minutes = int(v)
	}
	if minutes <= 0 {
		return false, ""
	}
	base := e.cfg.MacroBlackoutCurrencies
	if v, ok, err := e.store.AppConfigString(ctx, "ARB_MACRO_BLACKOUT_CURRENCIES"); err != nil {
		return false, ""
	} else if ok && strings.TrimSpace(v) != "" {
		base = v
	}
	curs := macro.BlackoutCurrencySet(base, canonical)
	now := time.Now()
	events, err := e.store.MacroHighEventsIn(ctx,
		now.Add(-time.Duration(minutes)*time.Minute), now.Add(time.Duration(minutes)*time.Minute), curs)
	if err != nil {
		return false, "" // fail-open：平仓侧不推迟
	}
	if len(events) == 0 {
		return false, ""
	}
	e2 := events[0]
	return true, fmt.Sprintf("macro_blackout: %s %s（BJ %s ±%dmin）",
		e2.Currency, e2.Name, e2.EventTime.In(macroBJLoc).Format("01-02 15:04"), minutes)
}

// macroBlackoutSuspend 平仓侧判定（Engine 域）：禁区内=true（调用方 markLegSuspend 跳过
// 本轮常规退出）。fail-open：查询/配置失败=false 照常评估（平仓侧不推迟）。
// D-053 R1②：XASSET_CARRY 恒 false——其禁区判定下沉 xassetExitCheck 链内（suspend
// 提前拦会连带推迟 disasterCheckXAsset 保命止损；链内判=carry_death 同闭市径冻结、
// harvest/execDrift/rotate 逐点跳过、disaster 仍跑，eval 落 macro_blackout）。
// 非 XASSET 类型退出链无 disaster，整段 suspend 推迟等价且留痕（markLegSuspend）。
func (e *Engine) macroBlackoutSuspend(ctx context.Context, p *pgstore.Position) (bool, string) {
	if p.Type == "XASSET_CARRY" {
		return false, ""
	}
	return e.macroBlackoutActive(ctx, e.positionCanonical(ctx, p))
}
