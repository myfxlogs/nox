package dashboard

// D-329 腿名义粗腿锚定匹配（两段式）。
// SSOT=docs/D-329+D-330设计与施工提示词-腿名义匹配与黄金demo放行-2026-09-18-Devin.md §2.1。
// resolveLegs 与 ExecuteMeanRevertWithCap 共用（同类对象一致性：两处失衡公式同源，消复制粘贴）。

import (
	"fmt"
	"log"
	"math"

	"arb/internal/bus"
	"arb/internal/keydomain"
)

// legStepQty 单腿最小步长（pass-2 粗腿锚定用；floorToStepQty 的 step 分派下沉复用）：
// MT=legVolumeStep（未接线/未知默认 0.01）；gate:perp=1.0（整数张协议事实）；
// okx:perp=instId→lotSz；binance=stepMeta 运行时+fallback；未知 venue → 0（fail-closed）。
func (s *Service) legStepQty(venueKey, symbol string) float64 {
	if isMTVenue(venueKey) {
		if s.legVolumeStep != nil {
			if vs, ok := s.legVolumeStep(venueKey, symbol); ok && vs > 0 {
				return vs
			}
		}
		return 0.01
	}
	switch {
	case venueKey == "gate:perp":
		return 1.0
	case venueKey == "okx:perp":
		if s.okxLotSz != nil {
			if l, ok := s.okxLotSz(symbol); ok && l > 0 {
				return l
			}
		}
	case keydomain.BareOf(venueKey) == "binance":
		return s.cexStepSize(symbol)
	}
	return 0
}

// matchLegsNotional 两段式腿名义匹配（D-329）：
//
// pass-1（原样，D-060 语义逐字节保留）：sellQty=round(qty×csBuy/csSell×100)/100 → 双腿
// floorToStepQty → 按买腿参考价统一计名义（D-028：防的是取整数量差，非 venue 价差）→
// imb≤maxImb 放行（平衡路径行为零变化）。
//
// pass-2（仅 imb>maxImb 触发）：粗腿（最小步名义 unitNotional=stepQty×cs×ref 大者）为锚不动，
// 细腿按锚回算 floorToStepQty 重算 imb——平衡即放行+matched 日志（匹配只缩不超锚，仓位可比
// intent 名义小，cap-safe）；仍超/细腿取整后≤0 → fail-fast「leg granularity unexecutable」
// （结构性粒度不可开仓：粗腿步长 ≳ 目标名义带，与原 imbalance 文案区分，digest 可分诊——重试无意义）。
func (s *Service) matchLegsNotional(venueBuy string, qbuy bus.Quote, csBuy float64,
	venueSell string, qsell bus.Quote, csSell float64, qty float64) (buyQty, sellQty float64, err error) {
	sellQty = math.Round(qty*csBuy/csSell*100) / 100
	buyQty = s.floorToStepQty(venueBuy, qbuy.VenueSymbol, qty)
	sellQty = s.floorToStepQty(venueSell, qsell.VenueSymbol, sellQty)
	ref := qbuy.Ask
	notionalBuy := buyQty * csBuy * ref
	notionalSell := sellQty * csSell * ref
	maxImb := s.maxLegImbalance
	if maxImb <= 0 {
		maxImb = 2.0
	}
	imb := math.Abs(notionalSell-notionalBuy) / notionalBuy
	if notionalBuy > 0 && imb <= maxImb/100 {
		return buyQty, sellQty, nil
	}
	// pass-2：粗腿锚定匹配——锚腿取 pass-1 量不动，细腿回算 floor 到锚名义以下
	buyUnit := s.legStepQty(venueBuy, qbuy.VenueSymbol) * csBuy * ref
	sellUnit := s.legStepQty(venueSell, qsell.VenueSymbol) * csSell * ref
	preBuy, preSell := buyQty, sellQty
	if buyUnit < sellUnit {
		buyQty = s.floorToStepQty(venueBuy, qbuy.VenueSymbol, sellQty*csSell/csBuy)
	} else {
		sellQty = s.floorToStepQty(venueSell, qsell.VenueSymbol, buyQty*csBuy/csSell)
	}
	notionalBuy = buyQty * csBuy * ref
	notionalSell = sellQty * csSell * ref
	imb = math.Abs(notionalSell-notionalBuy) / notionalBuy
	if notionalBuy <= 0 || notionalSell <= 0 || imb > maxImb/100 {
		return 0, 0, fmt.Errorf("leg granularity unexecutable: buy $%.2f vs sell $%.2f (coarse step $%.2f > tolerance at this notional)",
			notionalBuy, notionalSell, math.Max(buyUnit, sellUnit))
	}
	// D-329 R2 返修：matched 日志只在最终 imb 复检成功后打——失败路径误打 matched 误导 digest 分诊。
	if buyUnit < sellUnit {
		log.Printf("[exec] leg notional matched: %s qty %.4f→%.4f (coarse anchor %s)", venueBuy, preBuy, buyQty, venueSell)
	} else {
		log.Printf("[exec] leg notional matched: %s qty %.4f→%.4f (coarse anchor %s)", venueSell, preSell, sellQty, venueBuy)
	}
	return buyQty, sellQty, nil
}
