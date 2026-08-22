package dashboard

import (
	"strings"

	"arb/internal/fact"
	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- 转换辅助 ---

func factToProto(f *fact.Fact) *v1.Fact {
	return &v1.Fact{
		Kind:   f.Kind,
		Venue:  f.Venue,
		Symbol: f.Symbol,
		Value:  f.Value,
		Unit:   f.Unit,
		Ts:     timestamppb.New(f.Ts),
		Src:    f.Src,
	}
}

// netUSD 净收益 bps → USD（D-053）。与 net_bps 同语义：价差型=一次性（/10000×notional），
// 息差型=年化 USD/年（/100×notional，net_bps 为年化百分点，D-036）。notional≤0 时不换算（0）。
func netUSD(netBps float64, typ string, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	if strings.HasPrefix(typ, "CROSS_VENUE") {
		return netBps / 10000 * notional
	}
	return netBps / 100 * notional
}

func (s *Service) oppToProto(o *pgstore.Opportunity) *v1.Opportunity {
	pb := &v1.Opportunity{
		Id:             o.ID,
		Type:           o.Type,
		VenueBuy:       o.VenueBuy,
		VenueSell:      o.VenueSell,
		PriceBuy:       o.PriceBuy,
		PriceSell:      o.PriceSell,
		GrossBps:       o.GrossBps,
		SpreadCost:     o.SpreadCost,
		CommissionCost: o.CommissionCost,
		SlippageCost:   o.SlippageCost,
		SwapCost:       o.SwapCost,
		NetBps:         o.NetBps,
		NetUsd:         netUSD(o.NetBps, o.Type, s.notionalUSD),
		NotionalUsd:    s.notionalUSD,
	}
	// D-062 显示层 USD 派生（存储不动）。gap_usd_per_unit 无 CS 依赖恒填；
	// per-lot 字段依赖 venue_buy 侧 CS（D-061 对称桶保证跨 venue 一致），缺失/≤0 → 全 0（前端回退 bps）。
	pb.GapUsdPerUnit = o.PriceSell - o.PriceBuy
	if s.oppContractSize != nil {
		if cs, ok := s.oppContractSize(o.Canonical, o.VenueBuy); ok && cs > 0 && o.PriceBuy > 0 {
			totalCost := o.SpreadCost + o.CommissionCost + o.SlippageCost + o.SwapCost
			pb.CostUsdPerLot = totalCost / 1e4 * cs * o.PriceBuy
			if isCarryType(o.Type) {
				// 息差型：net_bps 为年化百分点 → USD/日/手 = net_bps/100 × CS × price / 365。
				pb.NetUsdPerLotDay = o.NetBps / 100 * cs * o.PriceBuy / 365
			} else {
				// 价差型：net_bps 为一次性 bps → USD/手 = net_bps/1e4 × CS × price。
				pb.NetUsdPerLot = o.NetBps / 1e4 * cs * o.PriceBuy
			}
		}
	}
	if o.BreakevenDays != nil {
		pb.BreakevenDays = o.BreakevenDays
	}
	if !o.QuoteTime.IsZero() {
		pb.QuoteTime = timestamppb.New(o.QuoteTime)
	}
	if !o.ExpiresAt.IsZero() {
		pb.ExpiresAt = timestamppb.New(o.ExpiresAt)
	}
	if !o.CreatedAt.IsZero() {
		pb.CreatedAt = timestamppb.New(o.CreatedAt)
	}
	if !o.UpdatedAt.IsZero() {
		pb.UpdatedAt = timestamppb.New(o.UpdatedAt)
	}
	return pb
}

// isCarryType 息差型判定（CARRY / FUNDING_HEDGE），与 netUSD 的 CROSS_VENUE 分支互补（D-062）。
func isCarryType(typ string) bool {
	return strings.HasPrefix(typ, "CARRY") || strings.HasPrefix(typ, "FUNDING_HEDGE")
}

func alertToProto(a *pgstore.Alert) *v1.Alert {
	pb := &v1.Alert{
		Id:       a.ID,
		RuleName: a.RuleName,
		Level:    a.Level,
		Message:  a.Message,
		Acked:    a.Acked,
	}
	if a.RuleID != nil {
		pb.RuleId = a.RuleID
	}
	if !a.Ts.IsZero() {
		pb.Ts = timestamppb.New(a.Ts)
	}
	return pb
}

func positionToProto(p *pgstore.Position) *v1.Position {
	pb := &v1.Position{
		Id:             p.ID,
		OpportunityId:  p.OpportunityID,
		Type:           p.Type,
		Status:         p.Status,
		ExpectedNetBps: p.ExpectedNetBps,
		Note:           p.Note,
	}
	if p.RealizedPnl != nil {
		pb.RealizedPnl = p.RealizedPnl
	}
	if p.OpenedAt != nil {
		pb.OpenedAt = timestamppb.New(*p.OpenedAt)
	}
	if p.ClosedAt != nil {
		pb.ClosedAt = timestamppb.New(*p.ClosedAt)
	}
	// D-080 S3：承诺年化%（可选，收敛型为 nil）
	if p.ExpectedAnnualPct != nil {
		pb.ExpectedAnnualPct = p.ExpectedAnnualPct
	}
	return pb
}

// positionLegToProto 将 position_legs 行转为 proto（D-069 成交历史）。
func positionLegToProto(l *pgstore.PositionLeg) *v1.PositionLeg {
	pb := &v1.PositionLeg{
		Id:            l.ID,
		PositionId:    l.PositionID,
		LegNo:         int32(l.LegNo),
		Phase:         l.Phase,
		Venue:         l.Venue,
		Account:       l.Account,
		Symbol:        l.Symbol,
		Side:          l.Side,
		Qty:           l.Qty,
		PriceExpected: l.PriceExpected,
		OrderId:       l.OrderID,
		Status:        l.Status,
		Ts:            timestamppb.New(l.Ts),
	}
	if l.PriceActual != nil {
		pb.PriceActual = l.PriceActual
	}
	return pb
}
