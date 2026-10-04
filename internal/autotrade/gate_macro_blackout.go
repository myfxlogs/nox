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

// isoCurrencies ISO 4217 币种/贵金属码集（canonical 拆半判定用；XAU/XAG=ISO 4217 贵金属段）。
// 与财历币种域（fetch_mql5 countryIDToCurrency）对齐 + 贵金属。
var isoCurrencies = map[string]bool{
	"USD": true, "EUR": true, "JPY": true, "GBP": true, "AUD": true,
	"CAD": true, "CHF": true, "NZD": true, "CNY": true, "HKD": true,
	"SGD": true, "KRW": true, "SEK": true, "NOK": true, "MXN": true,
	"XAU": true, "XAG": true,
}

// macroBlackoutCurrencies 禁区币种集推导：热读配置基集 ∪ canonical 两半（6 字母且均为 ISO）。
// 读取失败回落 cfg（boot env 值）；基集空串=缺省 USD。
func (g *Gate) macroBlackoutCurrencies(ctx context.Context, canonical string) []string {
	base := g.cfg.MacroBlackoutCurrencies
	if v, ok, err := g.store.AppConfigString(ctx, "ARB_MACRO_BLACKOUT_CURRENCIES"); err != nil {
		log.Printf("[autotrade] macro blackout currencies read: %v（回落 cfg）", err)
	} else if ok && strings.TrimSpace(v) != "" {
		base = v
	}
	if strings.TrimSpace(base) == "" {
		base = "USD"
	}
	set := map[string]bool{}
	for _, c := range strings.Split(base, ",") {
		if c = strings.ToUpper(strings.TrimSpace(c)); c != "" {
			set[c] = true
		}
	}
	if len(canonical) == 6 {
		a, b := strings.ToUpper(canonical[:3]), strings.ToUpper(canonical[3:])
		if isoCurrencies[a] && isoCurrencies[b] {
			set[a], set[b] = true, true
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	return out
}

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

// macroBlackoutSuspend 平仓侧判定（Engine 域）：禁区内=true（调用方 markLegSuspend 跳过
// 本轮常规退出）。fail-open：查询/配置失败=false 照常评估（平仓侧不推迟）。
// XASSET_CARRY 恒 false：disasterCheckXAsset（保命止损）位于其退出链内（xassetExitCheck
// 先决闸序），提前 suspend 会连带推迟 disaster——保命不推迟=宁早平不晚平，常规退出
// 推迟语义对该型让位（票面「disaster/止损路径不经此闸」风控正则）。
func (e *Engine) macroBlackoutSuspend(ctx context.Context, p *pgstore.Position) (bool, string) {
	if p.Type == "XASSET_CARRY" {
		return false, ""
	}
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
	set := map[string]bool{}
	for _, c := range strings.Split(base, ",") {
		if c = strings.ToUpper(strings.TrimSpace(c)); c != "" {
			set[c] = true
		}
	}
	canonical := e.positionCanonical(ctx, p)
	if len(canonical) == 6 {
		a, b := strings.ToUpper(canonical[:3]), strings.ToUpper(canonical[3:])
		if isoCurrencies[a] && isoCurrencies[b] {
			set[a], set[b] = true, true
		}
	}
	curs := make([]string, 0, len(set))
	for c := range set {
		curs = append(curs, c)
	}
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
