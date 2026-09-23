// gate_margin.go —— D-405 S4：开户门槛档（marginGuard 第三档）。
// venue 可用额 < 新仓所需保证金 → 拒 + info 告警。判定本体在 dashboard.MarginFloorCheck
// （数据源=fin_venue_account_snapshot 最新行 + OPEN 腿隐含保证金率），gate 只做单点挂接。
package autotrade

import (
	"context"
	"fmt"
	"log"
)

// marginFloorChecker 开户门槛检查器（dashboard.Service 实现；nil=未接线跳过——
// 与 Environment/LiveTrade 检查同款「未配置不检查」惯例）。
type marginFloorChecker interface {
	MarginFloorCheck(ctx context.Context, venueKey string, addNotionalUSD float64) (ok bool, detail string)
}

// SetMarginFloorChecker 注入开户门槛检查器（autotrade.New 装配）。
func (g *Gate) SetMarginFloorChecker(c marginFloorChecker) { g.marginFloor = c }

// marginFloorGateCheck 开户门槛：intent 双腿 venue 各查一次（去重）。
// 任一 venue 可用额不足 → 拒 + info 告警（SSOT §1 三档动作表）。
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
		ok, detail := g.marginFloor.MarginFloorCheck(ctx, vk, intent.NotionalUSD)
		if !ok {
			msg := fmt.Sprintf("开户门槛：venue %s 可用保证金不足（%s）——新仓被拒，建议补资金", vk, detail)
			if err := g.store.InsertAlert(ctx, nil, "info", msg); err != nil {
				log.Printf("[autotrade] margin floor alert insert: %v", err)
			}
			return GateVerdict{Pass: false, Reason: msg}
		}
	}
	return GateVerdict{Pass: true}
}
