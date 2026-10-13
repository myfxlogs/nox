// redeem.go —— 活期赎回面（D-203 拆出防 exec.go 超硬闸）：
// redeem 单点→RedeemFn 测试注口/redeemFlexible 真发；redeemShortfall 缺口逐行赎。
package dciexec

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"time"

	"nox/internal/platform/errclass"
	"nox/internal/store/pgstore"
)

func (e *Executor) redeem(ctx context.Context, productID string, amount float64) error {
	if e.RedeemFn != nil {
		return e.RedeemFn(ctx, productID, amount)
	}
	return e.redeemFlexible(ctx, productID, amount)
}

// redeemShortfall 活期赎回缺口：逐 flexible 持仓赎至凑齐 need（locked 定期不赎——
// 强制赎回罚息面禁动；redeemAll=true 全赎径禁——只赎缺口量）。
// productId 空/amt≤0|NaN→跳该行+note 如实报（键名以 venue 实拍为准）；
// 败 err 内嵌实发参回声 redeem(productId/amount/need) 禁签名密钥——返回值 notes。
// listRedeemPits 坑表读面（Store 缺席→fail-open 空集——赎回是补缺径非风险腿）。
func (e *Executor) listRedeemPits(ctx context.Context) ([]pgstore.VenueReject, error) {
	if e.Store == nil {
		return nil, nil
	}
	return e.Store.ListVenueRejects(ctx, "binance", "earn_redeem")
}

// learnRedeemPit 赎败落坑（Store 缺席→nil——测试注口径）。
func (e *Executor) learnRedeemPit(ctx context.Context, err error) error {
	if e.Store == nil {
		return nil
	}
	return e.Store.LearnVenueReject(ctx, "binance", "earn_redeem",
		errclass.VenueRejectSignature(err.Error()), 24*time.Hour)
}

func (e *Executor) redeemShortfall(ctx context.Context, asset string, need float64) (float64, []string, error) {
	// D-255 扩 op=earn_redeem：坑表 suppress/critical 窗内零 venue 打面
	// （同败因反复撞 redemption 与申购同规）；读败 fail-open——不堵补缺径。
	if pits, perr := e.listRedeemPits(ctx); perr == nil {
		for i := range pits {
			if pits[i].Active(e.Now()) && pits[i].Action != "fix" {
				return 0, nil, fmt.Errorf("坑表抑赎窗内（%s expires=%s）", pits[i].ErrorSignature, pitExpiry(&pits[i]))
			}
		}
	}
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
		if f, _ := strconv.ParseFloat(fmt.Sprintf("%.8f", amt), 64); f <= 0 {
			// venue amount %.8f——尘埃额格式化=0.00000000→-1102「amount empty」重放坑。
			notes = append(notes, fmt.Sprintf("跳过 %s 尘埃额 %.10f（格式化归零——发参即 -1102）", p.ProductID, amt))
			continue
		}
		if err := e.redeem(ctx, p.ProductID, amt); err != nil {
			if lerr := e.learnRedeemPit(ctx, err); lerr != nil {
				log.Printf("[dciexec] 赎回坑落行败: %v", lerr)
			}
			return done, notes, fmt.Errorf("redeem(productId=%q amount=%.8f need=%.8f): %w",
				p.ProductID, amt, need-done, err)
		}
		done += amt
	}
	return done, notes, nil
}
