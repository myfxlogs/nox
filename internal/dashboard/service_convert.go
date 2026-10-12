package dashboard

import (
	"context"
	"log"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
	"arb/internal/verification"

	v1 "arb/internal/gen/dashboardv1"

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

func (s *Service) oppToProto(ctx context.Context, o *pgstore.Opportunity) *v1.Opportunity {
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
		AliasBuy:       s.aliasForVenueKey(o.VenueBuy),  // D-175：买腿别名（空=回退 venue_buy）
		AliasSell:      s.aliasForVenueKey(o.VenueSell), // D-175：卖腿别名（空=回退 venue_sell）
	}
	// D-300：XASSET_CARRY 的 asset_class/canonical_sell（DB NULL → proto 空串）。
	if o.AssetClass != nil {
		pb.AssetClass = *o.AssetClass
	}
	if o.CanonicalSell != nil {
		pb.CanonicalSell = *o.CanonicalSell
	}
	// D-086 验证阶梯：后端按 vkey 聚合填入 verified_level/verify_ratio/verify_samples。
	s.fillVerifiedFields(pb, o)
	// D-318：结算口径参照（settle_net_bps/usd/samples，缺数据/无 perp 腿=缺省不填）。
	s.fillSettleRef(ctx, pb, o)
	// D-331：机会卡「已持仓」标识（活跃仓 opportunity_id 集合命中；store 未接线/查询失败=false fail-safe）。
	pb.HasOpenPosition = s.activeOpportunityIDs(ctx)[o.ID]
	// D-062 显示层 USD 派生（存储不动）。gap_usd_per_unit 无 CS 依赖恒填；
	// per-lot 字段依赖 venue_buy 侧 CS（D-061 对称桶保证跨 venue 一致），缺失/≤0 → 全 0（前端回退 bps）。
	pb.GapUsdPerUnit = o.PriceSell - o.PriceBuy
	pb.CostBps = o.SpreadCost + o.CommissionCost + o.SlippageCost + o.SwapCost // D-508 S8
	if s.oppContractSize != nil {
		if cs, ok := s.oppContractSize(o.Canonical, o.VenueBuy); ok && cs > 0 && o.PriceBuy > 0 {
			totalCost := o.SpreadCost + o.CommissionCost + o.SlippageCost + o.SwapCost
			pb.CostUsdPerLot = totalCost / 1e4 * cs * o.PriceBuy
			if isCarryType(o.Type) {
				// D-087 §3.4 typed 显示：SWAPARB 日 bps vs CARRY/FUNDING 年化百分点
				if o.Type == "SWAPARB" {
					// SWAPARB：net_bps 为日 swap bps → USD/日/手 = net_bps/1e4 × CS × price。
					pb.NetUsdPerLotDay = o.NetBps / 1e4 * cs * o.PriceBuy
				} else {
					// CARRY/FUNDING_HEDGE/PERP_FUNDING：net_bps 为年化百分点 → USD/日/手 = net_bps/100 × CS × price / 365。
					pb.NetUsdPerLotDay = o.NetBps / 100 * cs * o.PriceBuy / 365
				}
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

// isCarryType 息差型判定（D-087 §3.4 收敛：委托 verification.IsCarryType SSOT）。
// 含 SWAPARB/CARRY/FUNDING_HEDGE/PERP_FUNDING，与 netUSD 的 CROSS_VENUE 分支互补。
func isCarryType(typ string) bool {
	return verification.IsCarryType(typ)
}

// activeOppCacheTTL D-331：活跃仓 opportunity_id 集合缓存窗口。
const activeOppCacheTTL = 15 * time.Second

// activeOpportunityIDs D-331：有活跃仓位（OPENING/OPEN/LEGGED/CLOSING）的 opportunity_id
// 集合，15s TTL 缓存——机会列表/stream 出卡热路径防 N+1 直查。
// fail-safe（同 fillVerifiedFields 姿态）：store 未接线或查询失败 → 返回上次缓存
// （首次无缓存=空 map），err 打 log 不 panic。刷新整体替换 map 引用，调用方读旧引用安全。
func (s *Service) activeOpportunityIDs(ctx context.Context) map[string]bool {
	s.activeOppMu.Lock()
	defer s.activeOppMu.Unlock()
	if s.activeOppIDs == nil || time.Since(s.activeOppAt) > activeOppCacheTTL {
		if s.store != nil {
			ids, err := s.store.ActiveOpportunityIDs(ctx)
			if err != nil {
				log.Printf("[dashboard] active opportunity ids: %v (keep last cache)", err)
			} else {
				s.activeOppIDs = ids
				s.activeOppAt = time.Now()
			}
		}
	}
	if s.activeOppIDs == nil {
		return map[string]bool{}
	}
	return s.activeOppIDs
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
	if a.PositionID != nil {
		pb.PositionId = a.PositionID // D-411：持仓级告警透出（前端渲染不动，API 消费/联查用）
	}
	if !a.Ts.IsZero() {
		pb.Ts = timestamppb.New(a.Ts)
	}
	return pb
}

// positionToProto D-340 S3：改方法以挂年化对照填充（List/analyst 两消费路径同 helper P3）。
func (s *Service) positionToProto(ctx context.Context, p *pgstore.Position) *v1.Position {
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
	// D-340 S3：年化对照填充（OPEN 息差型 live / CLOSED realized 折算；缺省诚实空态）。
	s.fillCarryComparison(ctx, pb, p)
	// D-080 S3：承诺年化%（可选，收敛型为 nil）
	if p.ExpectedAnnualPct != nil {
		pb.ExpectedAnnualPct = p.ExpectedAnnualPct
	}
	// D-095：SWAPARB 日 swap bps 透传（typed 列 D-087 已建库，enrich 用此算应得）
	if p.ExpectedSwapBpsDaily != nil {
		pb.ExpectedSwapBpsDaily = p.ExpectedSwapBpsDaily
	}
	// D-047：PROBE 仓 decision_ref/auto_close_at 透传（其它型列 NULL → 缺省）。
	if p.DecisionRef != nil {
		pb.DecisionRef = p.DecisionRef
	}
	if p.AutoCloseAt != nil {
		pb.AutoCloseAt = timestamppb.New(*p.AutoCloseAt)
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
