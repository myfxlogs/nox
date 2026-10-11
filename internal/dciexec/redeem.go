// redeem.go —— 活期赎回面（D-203 拆出防 exec.go 超硬闸）：
// redeem 单点→RedeemFn 测试注口/redeemFlexible 真发；redeemShortfall 缺口逐行赎。
package dciexec

import (
	"context"
	"fmt"
	"math"
)

func (e *Executor) redeem(ctx context.Context, productID string, amount float64) error {
	if e.RedeemFn != nil {
		return e.RedeemFn(ctx, productID, amount)
	}
	return e.redeemFlexible(ctx, productID, amount)
}

// redeemShortfall 活期赎回缺口：逐 flexible 持仓赎至凑齐 need（locked 定期不赎——
// 强制赎回罚息面禁动；redeemAll=true 全赎径禁——只赎缺口量）。
// D-203②：productId 空/amt≤0|NaN→跳该行+note 如实报（键名以 venue 实拍为准）；
// 败 err 内嵌实发参回声 redeem(productId/amount/need) 禁签名密钥——返回值 notes。
func (e *Executor) redeemShortfall(ctx context.Context, asset string, need float64) (float64, []string, error) {
	eps, err := e.earnPositionsOf(ctx, asset)
	if err != nil {
		return 0, nil, err
	}
	var done float64
	var notes []string
	for _, p := range eps {
		if p.Locked || need-done <= 0 {
			continue
		}
		amt := p.Amount
		if amt > need-done {
			amt = need - done
		}
		if p.ProductID == "" {
			notes = append(notes, fmt.Sprintf("跳过 productId 空行（amount %.8f——venue 键名未中）", p.Amount))
			continue
		}
		if amt <= 0 || math.IsNaN(amt) {
			notes = append(notes, fmt.Sprintf("跳过 %s 异常量 %.8f", p.ProductID, amt))
			continue
		}
		if err := e.redeem(ctx, p.ProductID, amt); err != nil {
			return done, notes, fmt.Errorf("redeem(productId=%q amount=%.8f need=%.8f): %w",
				p.ProductID, amt, need-done, err)
		}
		done += amt
	}
	return done, notes, nil
}
