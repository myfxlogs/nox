// pipeline_notional.go —— D-032 R1 ②：pipeline 开仓落 positions.notional_usd=Σ腿名义。
//
// XASSET_CARRY/FUNDING_HEDGE 此前不写 notional_usd → D-032 权益纪律闸的 venue 在仓名义
// 恒按 NULL→0 低估，阈值漏计存量敞口。语义同 settlement.computeNotionalUSD：
// Σ OPEN FILLED 腿 qty×cs×price×fx（CEX 腿成交口径 qty 已是 base→cs=1；MT 腿 lots×CS；
// fx=canonical 计价币→USD，USD/USDT→1）。任一 FILLED 腿缺价/缺 CS/缺 fx → 整笔不写
// （NULL 诚实缺失，宁缺勿虚增——权益闸低估比虚增安全方向一致但不可半写）。
package exec

import (
	"context"
	"log"
	"time"

	"arb/internal/notional"
)

// writeOpenNotional 开仓腿结果→positions.notional_usd（D-032 R1）。
// 仅 XASSET_CARRY/FUNDING_HEDGE（SWAPARB 沿 engine_swaparb_armed 既有 2×intent 写口，不动）。
// 任意终态都写（Σ FILLED 腿名义是事实量）；零 FILLED/总额≤0 → 不写。
func (p *Pipeline) writeOpenNotional(ctx context.Context, posType, opportunityID string, posID int64, legs []LegSpec, results []LegResult) {
	if posType != "XASSET_CARRY" && posType != "FUNDING_HEDGE" {
		return
	}
	quoteCcy := notional.QuoteCcy(canonicalFromOppID(opportunityID))
	fx := 1.0
	if quoteCcy != "" && quoteCcy != "USD" {
		if p.fxResolver == nil {
			log.Printf("[exec] pos %d notional: fx resolver 未接线（%s）→ 不写", posID, quoteCcy)
			return
		}
		rate, ok := p.fxResolver.USDPer(ctx, quoteCcy, time.Now())
		if !ok || rate <= 0 {
			log.Printf("[exec] pos %d notional: fx 不可得（%s）→ 不写", posID, quoteCcy)
			return
		}
		fx = rate
	}
	total := 0.0
	for i, r := range results {
		if r.Status != "FILLED" || r.Qty <= 0 {
			continue
		}
		if r.Price <= 0 {
			log.Printf("[exec] pos %d notional: 腿 %s 成交价缺失 → 不写", posID, legs[i].VenueSymbol)
			return
		}
		cs := 1.0 // CEX 腿 LegResult.Qty 已是成交口径 base（:perp 经 orderQtyFor ×CS、:spot=base）。
		if isMTVenueKey(legs[i].VenueKey) {
			if p.csResolver == nil {
				log.Printf("[exec] pos %d notional: csResolver 未接线（MT 腿）→ 不写", posID)
				return
			}
			c, ok := p.csResolver.ContractSize(legs[i].VenueKey, legs[i].VenueSymbol)
			if !ok || c <= 0 {
				log.Printf("[exec] pos %d notional: MT CS 不可得（%s %s）→ 不写", posID, legs[i].VenueKey, legs[i].VenueSymbol)
				return
			}
			cs = c // MT 腿 Qty=lots → ×CS 折 canonical 单位
		}
		total += notional.LegUSD(r.Qty, cs, r.Price, fx)
	}
	if total <= 0 {
		return
	}
	if err := p.store.UpdatePositionNotional(ctx, posID, total); err != nil {
		log.Printf("[exec] pos %d notional write: %v", posID, err)
	}
}
