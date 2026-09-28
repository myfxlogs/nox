// gate_margin.go —— D-405 S4：开户门槛档（marginGuard 第三档）。
// venue 可用额 < 新仓所需保证金 → 拒 + info 告警。判定本体在 dashboard.MarginFloorCheck
// （数据源=D-549 R1 起执行面经 capitalview.View：CEX=活账本/REST 实拉，MT=快照即数据面
// + OPEN 腿隐含保证金率），gate 只做单点挂接 + CEX 资金源 Src 闸。
// D-549 R1 安全裁定：CEX 资金源 Src∈{ws,rest} 方可用；snap（陈旧快照兜底）=fail-closed
// 拒开——不降级用快照开真金仓（快照兜底仅限展示面）。
package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/capitalview"
	"arb/internal/errclass"
)

// marginFloorChecker 开户门槛检查器（dashboard.Service 实现；nil=未接线跳过——
// 与 Environment/LiveTrade 检查同款「未配置不检查」惯例）。
type marginFloorChecker interface {
	MarginFloorCheck(ctx context.Context, venueKey string, addNotionalUSD float64) (ok bool, detail string)
}

// SetMarginFloorChecker 注入开户门槛检查器（autotrade.New 装配）。
func (g *Gate) SetMarginFloorChecker(c marginFloorChecker) { g.marginFloor = c }

// SetCapitalView D-549 R1：注入统一资金读口（engineService.CapitalView 装配；
// 与 dashboard 共享一实例——REST 校准单飞语义；nil=未接线跳过 Src 闸）。
func (g *Gate) SetCapitalView(v *capitalview.View) { g.capView = v }

// SetVenueAlias D-556：注入账号别名读口（main 启动时 ListAccountAliases 一把加载闭包；
// 别名中途更新沿用 D-175 语义重启生效；nil=回落 bare 键）。
func (g *Gate) SetVenueAlias(aliasOf func(vk string) string) { g.venueAlias = aliasOf }

// marginFloorGateInfo 资金不足拒的白话化径由（与 Src 闸 info 严格分域——
// 资金拒=业主注资指引走 warn 节流；Src 闸=数据通道故障走 info，两类绝不混淆）。
func (g *Gate) marginFloorGateWarn(ctx context.Context, intent TradeIntent, vk, detail string) {
	msg := fmt.Sprintf("开户门槛：venue %s 可用保证金不足（%s）——新仓被拒，建议补资金", vk, detail)
	plain := fundingAlertText(intent, msg, g.venueAlias)
	if plain == "" {
		plain = fmt.Sprintf("【请加保证金】%s 套利开仓被拦下（%s）\n%s 可用保证金不足以再开新仓（%s）。请补充资金或等现有仓位释放。\n※ 同一组合+账户 6 小时内只提醒一次",
			intent.Canonical, intent.Strategy, accountLabel(vk, g.venueAlias), detail)
	}
	g.warnThrottled(ctx, intent, vk, plain)
}

// marginFloorGateCheck 开户门槛：intent 双腿 venue 各查一次（去重）。
// CEX venue 先过 capitalview Src 闸（ws/rest 方可用；snap=fail-closed 拒开，
// 检查器不进）；任一 venue 可用额不足/资金源陈旧 → 拒 + info 告警（SSOT §1 三档动作表）。
func (g *Gate) marginFloorGateCheck(ctx context.Context, intent TradeIntent) GateVerdict {
	if g.marginFloor == nil {
		return GateVerdict{Pass: true}
	}
	seen := map[string]bool{}
	for _, vk := range []string{intent.VenueBuy, intent.VenueSell} {
		if vk == "" || seen[vk] {
			continue
		}
		seen[vk] = true
		if g.capView != nil && capitalview.IsCEXVenue(vk) {
			f, ok := g.capView.Live(ctx, vk)
			if !ok || (f.Src != capitalview.SrcWS && f.Src != capitalview.SrcRest) {
				msg := fmt.Sprintf("开户门槛：venue %s 资金源非实拉（快照兜底/无数据）——执行面禁用陈旧快照，fail-closed 拒开；建议检查私有流/REST 凭据", vk)
				if err := g.store.InsertAlert(ctx, nil, "info", msg); err != nil {
					log.Printf("[autotrade] margin floor alert insert: %v", err)
				}
				return GateVerdict{Pass: false, Reason: msg}
			}
		}
		ok, detail := g.marginFloor.MarginFloorCheck(ctx, vk, intent.NotionalUSD)
		if !ok {
			// D-556：资金拒单=业主注资指引，改走节流 warn 通路（飞书可达；原 info 静默拒单收口）。
			msg := fmt.Sprintf("开户门槛：venue %s 可用保证金不足（%s）——新仓被拒，建议补资金", vk, detail)
			g.marginFloorGateWarn(ctx, intent, vk, detail)
			return GateVerdict{Pass: false, Reason: msg}
		}
	}
	return GateVerdict{Pass: true}
}

// marginWarnInterval D-472 S3：资金拒单业主告警节流窗（实测 XASSET FAILED 7天 8184 条
// 单类重复数十次——无节流=告警疲劳=业主不看；≥6h 至多一条，重复仅计数落 autotrade_log）。
const marginWarnInterval = 6 * time.Hour

// marginWarnThrottled D-472 S3：FAILED msg 含资金/强平语义子串 → warn 告警（节流）。
// 弃 errors.Is（B-005 P0：intent.Execute 返回字符串，err 对象已丢=哨兵在实现点必死），
// 改 msg 子串匹配——executor 统一拒文约定含 "margin insufficient"（okx/mt5 现文案已含，
// binance/gate 新写照此）；venue 原文 "Margin is insufficient" 与强平闸 "liq-distance"
// 大小写不敏感同规则匹配。
func (g *Gate) marginWarnThrottled(ctx context.Context, intent TradeIntent, msg string) {
	low := strings.ToLower(msg)
	if !strings.Contains(low, "margin insufficient") &&
		!strings.Contains(low, "margin is insufficient") &&
		!strings.Contains(low, "liq-distance") {
		return
	}
	// D-556：节流键追加解析出的腿 venue——同组合不同账户各提醒（业主要知道给谁注资）。
	leg := alertLeg(msg, intent.VenueBuy, intent.VenueSell)
	if plain := fundingAlertText(intent, msg, g.venueAlias); plain != "" {
		g.warnThrottled(ctx, intent, leg, plain)
		return
	}
	g.warnThrottled(ctx, intent, leg,
		fmt.Sprintf("[资金拒单] %s %s net=%.1fbps: %s（≥6h 节流，重复见 autotrade_log）",
			intent.Strategy, intent.Canonical, intent.NetBps, errclass.TruncateNote(msg, 120)))
}

// warnThrottled D-472 节流本体（D-556 起 key=canonical|buy|sell|leg venue）：
// ≥6h 至多一条 warn；重复仅 autotrade_log 留痕（上方已落库）。
func (g *Gate) warnThrottled(ctx context.Context, intent TradeIntent, legVK, plain string) {
	key := intent.Canonical + "|" + intent.VenueBuy + "|" + intent.VenueSell + "|" + legVK
	g.lastMarginWarnMu.Lock()
	now := time.Now()
	if last, ok := g.lastMarginWarn[key]; ok && now.Sub(last) < marginWarnInterval {
		g.lastMarginWarnMu.Unlock()
		return // 节流窗内：仅 autotrade_log 留痕（上方已落库），不重发告警
	}
	g.lastMarginWarn[key] = now
	g.lastMarginWarnMu.Unlock()
	if err := g.store.InsertAlert(ctx, nil, "warn", plain); err != nil {
		log.Printf("[autotrade] margin warn insert: %v", err)
	}
}
