// service_positions_enrich_closenet.go D-480：平仓净估（close_now_net）——
// 「若此刻市价平仓到手净额」= Σ 腿出口价浮动 + swap 实收 + 已付佣金 − Σ 腿退出成本估。
// 与 net_pnl_usd（只含已发生成本）语义分离：net_pnl 账面，close_now_net 回本判定口径。
package dashboard

import (
	"context"
	"log"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/notional"
	"arb/internal/store/pgstore"
)

// closeNowNet enrichOne 尾部补算（仅 matched||funding!=0 门内调用）。
// ok=false → 整列 NULL（任一 CEX 腿报价缺=fail-closed 不猜）。
//   - 出口价浮动：CEX 腿 fresh 报价按退出侧取价（BUY→bid 卖出、SELL→ask 买回）重算 legUSD 差；
//     报价超窗 → venue 快照 profit 近似 + est log 注记；MT 腿恒用快照 profit（venue 权威市价）。
//   - 退出成本：CEX 腿=腿名义×takerBps（commissionFor→xassetCexFeeBps 回退链，与 D-473 现金预检同表）；
//     费率双缺→该腿 0+log 不阻塞；MT 腿=0（平仓费=点差内嵌，XAU 标准户零佣金实证）。
func (s *Service) closeNowNet(ctx context.Context, pb *v1.Position, openLegs []pgstore.PositionLeg, brokerSnap []*v1.BrokerPosition, swapAccrued, commission float64, now time.Time) (float64, float64, bool) {
	canonical := canonicalFromOppID(pb.OpportunityId)
	quoteCcy := notional.QuoteCcy(canonical)
	exitFloat, exitCost := 0.0, 0.0
	for _, l := range openLegs {
		if l.Status != "FILLED" {
			continue // 未成交腿无持仓无浮动
		}
		if !isCEXVenueKey(l.Venue) {
			exitFloat += snapLegProfit(brokerSnap, l, pb.Id) // MT 腿沿用快照 profit；退出费=0 点差内嵌
			continue
		}
		if s.qbus == nil {
			return 0, 0, false // CEX 腿缺报价源→整列 NULL
		}
		// D-480 R1：qbus 键=venue 原生 symbol 归一（采集器产出权威键，okx XAU-USDT-SWAP→XAUUSDT），
		// 非 oppID canonical（mt×cex 型 canonical=XAU 永不命中）——原生键优先，canonical 兜底。
		symKey := canonical
		if nk := cexCanonical(l.Symbol); nk != "" {
			symKey = nk
		}
		q, ok := s.qbus.Latest(symKey, l.Venue)
		if !ok && symKey != canonical {
			q, ok = s.qbus.Latest(canonical, l.Venue)
		}
		if !ok {
			log.Printf("[enrich] pos=%d leg %s %s 报价缺（symKey=%s canonical=%s）→close_now_net NULL", pb.Id, l.Venue, l.Symbol, symKey, canonical)
			return 0, 0, false // 报价缺→整列 NULL
		}
		if now.Sub(q.Time) <= s.freshTTL {
			exitPx := q.Bid // BUY 腿平仓卖→bid
			if l.Side == "SELL" {
				exitPx = q.Ask // SELL 腿平仓买回→ask
			}
			entryPx := l.PriceExpected
			if l.PriceActual != nil {
				entryPx = *l.PriceActual
			}
			if exitPx <= 0 || entryPx <= 0 {
				return 0, 0, false
			}
			usdExit := s.legUSDAt(ctx, l, canonical, quoteCcy, exitPx)
			usdEntry := s.legUSDAt(ctx, l, canonical, quoteCcy, entryPx)
			if usdExit <= 0 || usdEntry <= 0 {
				return 0, 0, false // CS/fx 缺→不猜
			}
			legFloat := usdExit - usdEntry
			if l.Side == "SELL" {
				legFloat = -legFloat
			}
			exitFloat += legFloat
		} else {
			exitFloat += snapLegProfit(brokerSnap, l, pb.Id)
			log.Printf("[enrich] pos=%d leg %s %s 报价超窗→快照近似（est）", pb.Id, l.Venue, l.Symbol)
		}
		bps := 0.0
		if s.commissionFor != nil {
			bps = s.commissionFor(l.Venue)
		}
		if bps <= 0 {
			bps = s.xassetCexFeeBps
		}
		if bps <= 0 {
			log.Printf("[enrich] pos=%d leg %s %s 退出费率双缺→成本按 0 估（est）", pb.Id, l.Venue, l.Symbol)
			continue
		}
		if n := s.legNotionalUSD(ctx, l, canonical, quoteCcy); n > 0 {
			exitCost += n * bps / 10000
		}
	}
	return exitFloat + swapAccrued + commission - exitCost, exitCost, true
}

// snapLegProfit 单腿 venue 快照浮动（venueKey+symbol+positionId 谓词同 matchBrokerLegs）。
func snapLegProfit(brokerSnap []*v1.BrokerPosition, l pgstore.PositionLeg, posID int64) float64 {
	sum := 0.0
	for _, bp := range brokerSnap {
		if bp.VenueKey == l.Venue && bp.Symbol == l.Symbol && bp.PositionId == posID {
			sum += bp.Pnl
		}
	}
	return sum
}
