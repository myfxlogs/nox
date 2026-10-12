// gate_macro_blackout.go —— 财历禁区平仓侧判定（开仓侧 gate.Gate 件移 internal/risk/gate，D-212 拆搬）。
package autotrade

import (
	"context"
	"fmt"
	"strings"
	"time"

	"nox/internal/data/macro"
	"nox/internal/store/pgstore"
)

// macroBJLoc 禁区 reason 的 BJ 时刻坐标（macro/gate 包同值独立定义——包间不互引）。
var macroBJLoc = func() *time.Location {
	l, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return l
}()

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
	if v, ok, err := e.store.AppConfigInt64(ctx, "NOX_MACRO_BLACKOUT_MIN"); err != nil {
		return false, "" // fail-open
	} else if ok {
		minutes = int(v)
	}
	if minutes <= 0 {
		return false, ""
	}
	base := e.cfg.MacroBlackoutCurrencies
	if v, ok, err := e.store.AppConfigString(ctx, "NOX_MACRO_BLACKOUT_CURRENCIES"); err != nil {
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
// R1②：XASSET_CARRY 恒 false——其禁区判定下沉 xassetExitCheck 链内（suspend
// 提前拦会连带推迟 disasterCheckXAsset 保命止损；链内判=carry_death 同闭市径冻结、
// harvest/execDrift/rotate 逐点跳过、disaster 仍跑，eval 落 macro_blackout）。
// 非 XASSET 类型退出链无 disaster，整段 suspend 推迟等价且留痕（markLegSuspend）。
func (e *Engine) macroBlackoutSuspend(ctx context.Context, p *pgstore.Position) (bool, string) {
	if p.Type == "XASSET_CARRY" {
		return false, ""
	}
	return e.macroBlackoutActive(ctx, e.positionCanonical(ctx, p))
}
