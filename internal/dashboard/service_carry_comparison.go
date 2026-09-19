// service_carry_comparison.go D-340 S3：持仓年化对照填充（自 service_convert.go 拆出保行预算）。
// live=引擎包装（与退出判定 recalcXAssetNetCarry 同源，15s TTL）；realized=realized 折算。
package dashboard

import (
	"context"
	"time"

	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"strings"
)

// carryLiveTTL D-340 S3：live 值缓存窗口（OPEN 息差型 ≤2 仓量级；与 ActiveOpportunityIDs 15s 先例同规）。
const carryLiveTTL = 15 * time.Second

// fillCarryComparison D-340 S3：年化对照填充——
// OPEN+XASSET_CARRY：live=引擎包装（15s TTL；fail-closed 缺省，与退出判定同病同药）；
// CLOSED+realized_pnl：realized_annual=realized/(Σ OPEN 腿 qty×usdPerLot)×365/hold×100
// （usdPerLot 走 USDPerLotForLeg 单点；任一缺→缺省诚实空态；hold<1 缺省=D-340R2 短仓护栏）。
// entryGateSnapshotMaxAge D-341 S1：开仓时点 monitor 行新鲜度（同 D-335 闸 30min 容差）。
const entryGateSnapshotMaxAge = 30 * time.Minute

func (s *Service) fillCarryComparison(ctx context.Context, pb *v1.Position, p *pgstore.Position) {
	switch {
	case p.Status == "OPEN" && p.Type == "XASSET_CARRY" && s.xassetCarryLive != nil:
		if v, ok := s.cachedCarryLive(ctx, p.ID); ok {
			pb.LiveNetCarryPct = &v
		}
	case p.Status == "CLOSED" && p.RealizedPnl != nil && p.OpenedAt != nil && p.ClosedAt != nil:
		holdDays := p.ClosedAt.Sub(*p.OpenedAt).Hours() / 24
		if holdDays < 1 { // D-340R2：不足一日不折年化——短仓分母→0 年化爆炸（491 实证 −6417%），绝对盈亏在 realized 字段可见
			return
		}
		legs, err := s.store.ListPositionLegs(ctx, p.ID)
		if err != nil {
			return
		}
		// 腿 canonical：BUY=opp.canonical、SELL=canonical_sell（D-328 同款空回退）。
		buyCanon, sellCanon := p.OpportunityID, p.OpportunityID
		if opp, err := s.store.GetOpportunity(ctx, p.OpportunityID); err == nil && opp != nil {
			buyCanon = opp.Canonical
			sellCanon = buyCanon
			if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
				sellCanon = *opp.CanonicalSell
			}
		}
		var notional float64
		for _, l := range legs {
			if l.Phase != "OPEN" || l.Qty <= 0 {
				continue
			}
			canonical := buyCanon
			if l.Side == "SELL" {
				canonical = sellCanon
			}
			perLot, ok := s.USDPerLotForLeg(canonical, l.Venue, legSideForQuote(l.Side))
			if !ok || perLot <= 0 {
				return // 任一腿名义不可得 → 缺省（不猜）
			}
			notional += perLot * l.Qty
		}
		if notional <= 0 {
			return
		}
		v := *p.RealizedPnl / notional * 365 / holdDays * 100
		pb.RealizedAnnualPct = &v
	}
}

// legSideForQuote 腿方向→报价侧（BUY 用 ask 开仓口径 / SELL 用 bid）。
func legSideForQuote(side string) string {
	if side == "SELL" {
		return "bid"
	}
	return "ask"
}

// cachedCarryLive 15s TTL 包装（两次渲染一次引擎，D-331 缓存先例）。
func (s *Service) cachedCarryLive(ctx context.Context, posID int64) (float64, bool) {
	s.carryMu.Lock()
	if v, ok := s.carryLive[posID]; ok && time.Since(s.carryLiveAt[posID]) < carryLiveTTL {
		s.carryMu.Unlock()
		return v, true
	}
	s.carryMu.Unlock()
	v, ok := s.xassetCarryLive(ctx, posID)
	s.carryMu.Lock()
	if ok {
		if s.carryLive == nil {
			s.carryLive = map[int64]float64{}
			s.carryLiveAt = map[int64]time.Time{}
		}
		s.carryLive[posID] = v
		s.carryLiveAt[posID] = time.Now()
	} else {
		if s.carryLive == nil {
			s.carryLive = map[int64]float64{}
			s.carryLiveAt = map[int64]time.Time{}
		}
		// fail-closed 也计入冷却，防断供期每渲染重打引擎
		s.carryLiveAt[posID] = time.Now()
	}
	s.carryMu.Unlock()
	return v, ok
}

// ensureEntryGateSnapshot D-341 S1：ExecuteOpportunityInternal 写点——
// pairKey=D-335 同款（CanonicalSell-TrimSuffix(Canonical,"USD")）。
func (s *Service) ensureEntryGateSnapshot(ctx context.Context, posID int64, found *pgstore.Opportunity) {
	sellCanon := found.Canonical
	if found.CanonicalSell != nil && *found.CanonicalSell != "" {
		sellCanon = *found.CanonicalSell
	}
	pairKey := sellCanon + "-" + strings.TrimSuffix(found.Canonical, "USD")
	_ = s.store.EnsureEntryGateSnapshot(ctx, posID, pairKey, entryGateSnapshotMaxAge)
}

// ensureEntryGateSnapshotCanonical D-341 S1：MeanRevertWithCap 写点（双腿同 canonical 兜底形态）。
func (s *Service) ensureEntryGateSnapshotCanonical(ctx context.Context, posID int64, canonical string) {
	_ = s.store.EnsureEntryGateSnapshot(ctx, posID, canonical+"-"+strings.TrimSuffix(canonical, "USD"), entryGateSnapshotMaxAge)
}
