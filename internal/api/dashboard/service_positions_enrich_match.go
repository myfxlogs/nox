package dashboard

// broker position 匹配 + 名义价值计算。
// matchBrokerLegs 按 venueKey+symbol+positionId 唯一匹配（D-117 修复 D-095 double-counting）。

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"arb/internal/data/keydomain"
	"arb/internal/platform/notional"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"
)

// exitFeeWarnOnce D-093：venue 费率键缺失 warn 只打一次（进程级；enrich 周期性重算防刷屏）。
var exitFeeWarnOnce sync.Map

// exitFeeBpsFor D-093 R1：venue 出场 taker 费率（app_config 明文热轨）——读 acv.running
// 视图 typed 字段（面板改后热换 running 即效，service_appconfig 写同步）；未配置/视图
// 未装配 → (0,false)，调用方费 0+warn-once。v1 单键 ARB_EXIT_FEE_BPS_OKX（gate/binance
// 待实证再扩字段）。
func (s *Service) exitFeeBpsFor(venueKey string) (float64, bool) {
	if s.acv.running == nil {
		return 0, false
	}
	switch keydomain.BareOf(venueKey) {
	case "okx":
		bps := s.acv.running.ExitFeeBpsOKX
		return bps, bps > 0
	default:
		return 0, false
	}
}

// exitFeeEstUSD D-093 S1：平仓出场费估算 Σ——每条 OPEN 且 FILLED 的 CEX 腿
// legNotional(入口实价) × takerBps[venue]/1e4。费率源=app_config 热键
// ARB_EXIT_FEE_BPS_OKX（明文热轨，面板改后即效）；无配置键 → 该腿
// 费 0 + log-once warn（fail-closed 显式，不静默当免费）。MT 腿不加费项：Exness 实证
// 佣金 0、点差成本已在 broker floating（按对手价计浮盈）内。legNotional=qty×price_actual
// （legNotionalUSD 同源 cs/fx 折算；actual 缺→0 不猜——v1 口径价漂移误差几 % 内可接受）。
func (s *Service) exitFeeEstUSD(ctx context.Context, pb *v1.Position, openLegs []pgstore.PositionLeg) float64 {
	canonical := canonicalFromOppID(pb.OpportunityId)
	quoteCcy := notional.QuoteCcy(canonical)
	total := 0.0
	for _, l := range openLegs {
		if l.Status != "FILLED" || !isCEXVenueKey(l.Venue) {
			continue
		}
		bps, ok := s.exitFeeBpsFor(l.Venue)
		if !ok {
			if _, done := exitFeeWarnOnce.LoadOrStore(l.Venue, struct{}{}); !done {
				log.Printf("[enrich] exit fee 未配置 venue=%s（ARB_EXIT_FEE_BPS_%s 缺或 0）→ 该腿平仓费按 0 估",
					l.Venue, strings.ToUpper(keydomain.BareOf(l.Venue)))
			}
			continue
		}
		if n := s.legNotionalUSD(ctx, l, canonical, quoteCcy); n > 0 {
			total += n * bps / 10000
		}
	}
	return total
}

// filterOpenLegs 取 OPEN phase 腿（匹配 broker position 用）。
func filterOpenLegs(legs []pgstore.PositionLeg) []pgstore.PositionLeg {
	var out []pgstore.PositionLeg
	for _, l := range legs {
		if l.Phase == "OPEN" {
			out = append(out, l)
		}
	}
	return out
}

// matchBrokerLegs 按 OPEN 腿 venueKey+symbol+positionId 匹配 broker position，求 Σ profit / Σ swap / Σ commission。
// 返回 (floating, swapAccrued, commission, matched)；matched=false = 全部腿未匹配。
// D-117：加 positionId 约束（避免同 venue+symbol 跨 position double-counting，D-095 遗留 bug）；
//
//	加 commission 返回值（net = floating + swap + commission，含佣金成本）。
//
// broker position 的 positionId 由 annotatePositionID 标注（(venueKey,ticket)→posID 索引）；
// positionId=0（孤儿订单/未关联）不匹配任何 leg（fail-closed，不冒充）。
func matchBrokerLegs(openLegs []pgstore.PositionLeg, brokerSnap []*v1.BrokerPosition, posID int64) (float64, float64, float64, bool) {
	var floating, swap, commission float64
	matched := false
	for _, leg := range openLegs {
		for _, bp := range brokerSnap {
			if bp.VenueKey == leg.Venue && bp.Symbol == leg.Symbol && bp.PositionId == posID {
				floating += bp.Pnl
				swap += bp.Swap
				commission += bp.Commission
				matched = true
			}
		}
	}
	return floating, swap, commission, matched
}

// fundingForLegs D-322 S3b：CEX 腿 funding 按 venue 去重并入回本口径。
// 去重谓词与 matchBrokerLegs 同型（venueKey+symbol+positionId）：该腿匹配到 broker position
// 且 bp.Swap≠0（venue 实报收讫，已计入 swapAccrued）→ 跳过该 venue；
// 未匹配或 bp.Swap=0（binance/okx 无 Swap 字段恒 0）→ 兜底计入。
// D-375 S2：兜底源由 cashflow 估算换 income_events 实收（estimated cashflow 退出已收列；
// collector 插入期已挂 position_id/leg_no，展示侧不重做匹配）。
// MT 腿不入账（swap 走 broker 快照，口径同 D-146 S5 前）；每 venue 只计一次（income 按
// (pos,venue) 聚合，多腿同 venue 会重复累加）。sums=SumIncomeByPosVenue 预取（key=posID\x00venue）。
func fundingForLegs(posID int64, openLegs []pgstore.PositionLeg, brokerSnap []*v1.BrokerPosition, sums map[string]float64) float64 {
	total := 0.0
	counted := make(map[string]bool)
	for _, leg := range openLegs {
		if !isCEXVenueKey(leg.Venue) || counted[leg.Venue] {
			continue
		}
		counted[leg.Venue] = true
		venueReported := false
		for _, bp := range brokerSnap {
			if bp.VenueKey == leg.Venue && bp.Symbol == leg.Symbol && bp.PositionId == posID && bp.Swap != 0 {
				venueReported = true
				break
			}
		}
		if venueReported {
			continue // venue 真值已在 swapAccrued（matchBrokerLegs Σ swap），估算不双计
		}
		total += sums[fmt.Sprintf("%d\x00%s", posID, leg.Venue)]
	}
	return total
}

// computeEntryNotional Σ OPEN FILLED 腿 LegUSD（CS+fx 经 store 查询）。
// 与 settlement.computeNotionalUSD 同构（P3 复用 notional.LegUSD/QuoteCcy/VenueAccount，不复制实现）。
// 缺 CS/fx → 该腿跳过（notional 可能部分算）；全缺 → 返回 0（应得缺省）。
func computeEntryNotional(ctx context.Context, s *Service, openLegs []pgstore.PositionLeg, canonical string) float64 {
	if s.store == nil || canonical == "" {
		return 0
	}
	quoteCcy := notional.QuoteCcy(canonical)
	total := 0.0
	for _, l := range openLegs {
		total += s.legNotionalUSD(ctx, l, canonical, quoteCcy)
	}
	return total
}

// legNotionalUSD 单腿名义 USD（computeEntryNotional 的逐腿因子，D-391 拆出供 fee 估算回退复用）。
// 缺 CS → cs=1.0；非 USD 计价缺 fx → 该腿计 0。
func (s *Service) legNotionalUSD(ctx context.Context, l pgstore.PositionLeg, canonical, quoteCcy string) float64 {
	if l.PriceActual == nil || *l.PriceActual <= 0 {
		return 0
	}
	return s.legUSDAt(ctx, l, canonical, quoteCcy, *l.PriceActual)
}

// legUSDAt 单腿按指定价折 USD（D-480 出口价浮动复用同口径——cs/fx 解析同 legNotionalUSD）。
func (s *Service) legUSDAt(ctx context.Context, l pgstore.PositionLeg, canonical, quoteCcy string, px float64) float64 {
	if l.Status != "FILLED" || px <= 0 || l.Qty <= 0 {
		return 0
	}
	// D-594：px=原生成交价 → 必须配原生 CS（勿走 oppContractSize——其已升 canonical
	// 单位数/手口径配机会卡 canonical 价）。
	cs := 1.0
	if s.legContractSize != nil {
		if c, ok := s.legContractSize(l.Venue, l.Symbol); ok && c > 0 {
			cs = c
		}
	} else if s.store != nil {
		if c, ok, _ := s.store.ContractSizeByCanonical(ctx, l.Venue, canonical); ok && c > 0 {
			cs = c
		}
	}
	fx := 1.0
	if quoteCcy != "" && quoteCcy != "USD" {
		if s.store == nil {
			return 0
		}
		rate, ok := s.store.USDPer(ctx, quoteCcy, l.Ts)
		if !ok {
			return 0
		}
		fx = rate
	}
	return notional.LegUSD(l.EffectiveQty(), cs, px, fx) // D-311 R6：成交口径（CEX=基础币）
}
