package dashboard

// D-095 持仓表第一性重构：enrichPositions 对活跃行派生 7 个 optional 字段。
// 设计 SSOT：docs/持仓表第一性重构设计.md §3（零信任——派生字段全部后端）。
//
// 派生口径：
//   - floating_pnl_usd / swap_accrued_usd / net_pnl_usd：bpm.Snapshot() 按 OPEN 腿 venueKey+symbol+positionId 匹配 broker position，
//     Σ profit→floating、Σ swap→swap_accrued、和→net（任一腿未匹配即该腿两项不计入，全部未匹配→三字段缺省）；
//   - expected_swap_usd_to_date：D-118 结算日历感知——dailySwap × (结算天数 + 2×三倍日数)；
//   - attainment_pct：swap_accrued / expected_swap_usd_to_date × 100（应得≤0 或 swap 缺省→缺省）；
//   - current_net_bps：ListComboDaily(comboKey, 1) 最新 net_bps（收敛型）。
//
// holdDays = now − opened_at（活跃行无 closed_at；与 settlement.computeHoldDays 同构但终点=now 非 closed_at，注释指认不复制）。
// 名义 = Σ notional.LegUSD(qty, CS, PriceActual, fx)，CS=store.ContractSizeByCanonical、fx=store.USDPer（D-087 计算器同构）。
//
// 拆分文件：_calendar.go（D-118 结算日历）+ _match.go（broker 匹配+名义）+ _helpers.go（持有天数+canonical+bps）。

import (
	"context"
	"log"
	"math"
	"time"

	"arb/internal/notional"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"
)

// activeStatuses 活跃态集合（enrich 仅对这些行计算；CLOSED/FAILED 只填 realized 既有）。
var activeStatuses = map[string]bool{
	"OPENING": true, "OPEN": true, "LEGGED": true, "CLOSING": true,
}

// carryTypes 息差型（填 12/16/17）；收敛型填 18，互不越界。
// XASSET_CARRY（D-311 R7①）：§1 回本周期启用；2b settleDays 分支由 ExpectedSwapBpsDaily 门控不受影响。
var carryTypes = map[string]bool{
	"SWAPARB": true, "CARRY": true, "FUNDING_HEDGE": true, "PERP_FUNDING": true, "XASSET_CARRY": true,
}

// convergentTypes 收敛型（填 current_net_bps）。
var convergentTypes = map[string]bool{
	"CROSS_VENUE": true, "MEANREVERT": true,
}

// enrichPositions 对活跃行计算 7 派生字段，原地修改 pb positions。
// bpm 为 nil（测试未接线）时整组跳过；legs 按 positionID 批量预取。
// D-117：brokerSnap 需标注 positionId（与 ListBrokerPositions 同源），matchBrokerLegs 按 positionId 唯一匹配。
// D-118：批量预取 per-venue triple_swap_day（facts 表），传给 enrichOne 算结算日历感知预期 swap。
// D-146 S5：批量预取 funding 实收（position_cashflows），传给 enrichOne 扩展回本口径；
// D-322 S3b：预取改 per-(pos,venue) 合计，enrichOne 按 venue 对照 broker 实报 swap 去重。
func (s *Service) enrichPositions(ctx context.Context, pbPositions []*v1.Position, legsByPos map[int64][]pgstore.PositionLeg) {
	if s.bpm == nil {
		return // 测试未接线 → 整组跳过（诚实缺省，不伪造）
	}
	brokerSnap := s.bpm.Snapshot()
	annotatePositionID(ctx, s.store, brokerSnap) // D-117：标注 positionId（与 ListBrokerPositions 同源）
	// D-118：批量预取 per-venue triple_swap_day（从 OPEN legs 提取 venue 列表）
	tripleDayByVenue := s.prefetchTripleSwapDays(ctx, pbPositions, legsByPos)
	// D-146 S5 + D-322 S3b：批量预取 per-(pos,venue) funding 合计（CEX 仓回本口径扩展）。
	// D-375 S2：换源实收——income 实收 + estimated 公式计提双预取（估行退参考位）。
	incomeByPosVenue, estimatedByPos := s.prefetchSwapSources(ctx, pbPositions)
	now := time.Now()
	for _, pb := range pbPositions {
		if !activeStatuses[pb.Status] {
			continue // CLOSED/FAILED 不 enrich
		}
		legs := legsByPos[pb.Id]
		enrichOne(ctx, s, pb, legs, brokerSnap, now, tripleDayByVenue, incomeByPosVenue, estimatedByPos)
	}
}

// prefetchSwapSources D-375 S2（前身为 prefetchFundingCashflows D-146 S5/D-322 S3b）：批量预取
// 活跃持仓两源——①incomeByPosVenue=income_events 实收合计（SumIncomeByPosVenue，key=posID\x00venue）
// ②estimatedByPos=cashflows estimated 公式计提合计（SumEstimatedCashflowsByPos，key=posID）。
// 实收入「息差已收」列（venue 实报优先去重同 D-322 S3b）；estimated 退参考行（前端「估」小字）。
// store=nil（测试未接线）→ 双空 map（enrichOne 无加成，纯 MT 仓行为不变）。
// 查询失败 → 各自空 map（fail-closed：实收缺失不冒充、估值缺失不伪造）。
func (s *Service) prefetchSwapSources(ctx context.Context, pbPositions []*v1.Position) (map[string]float64, map[int64]float64) {
	income := make(map[string]float64)
	estimated := make(map[int64]float64)
	if s.store == nil {
		return income, estimated // 测试未接线 → 双空 map
	}
	seen := make(map[int64]bool)
	posIDs := make([]int64, 0, len(pbPositions))
	for _, pb := range pbPositions {
		if !activeStatuses[pb.Status] || seen[pb.Id] {
			continue
		}
		seen[pb.Id] = true
		posIDs = append(posIDs, pb.Id)
	}
	incomeSums, err := s.store.SumIncomeByPosVenue(ctx, posIDs)
	if err == nil {
		income = incomeSums
	} // fail-closed：实收查询失败视为无流，不冒充
	estSums, err := s.store.SumEstimatedCashflowsByPos(ctx, posIDs)
	if err == nil {
		estimated = estSums
	}
	return income, estimated
}

// enrichOne 单行派生（拆出便于测试）。
// D-118：tripleDayByVenue 为 per-venue triple_swap_day 预取 map（可能空，enrichOne 退化为纯结算天数）。
// D-375 S2：incomeByPosVenue=income 实收预取（key=posID\x00venue，可能空）；estimatedByPos=
// cashflows estimated 公式计提预取（key=posID，可能空）。
//
// 已收口径（D-146 S5 + D-322 S3b + D-375 换源）：
//   - swapAccrued = broker 快照 Σ swap + income 实收（per-venue 去重：venue 实报≠0 的腿
//     只计 venue 真值，income 跳过；venue 未报才用 income 兜底。estimated cashflow 退出本列）
//   - swapEstimatedUsd = estimated 合计（>0 即填，独立于 matched 门控——参考行不因实收缺失隐藏）
//   - net = floating + commission + swapAccrued（估算退出净值）
//   - matched=false 但 funding>0 → 仍出值（CEX 仓有实收流→回本显示数值）
//   - matched=false 且 funding=0 → 三字段缺省（零实收仓仍「—」+「估」行）
func enrichOne(ctx context.Context, s *Service, pb *v1.Position, legs []pgstore.PositionLeg, brokerSnap []*v1.BrokerPosition, now time.Time, tripleDayByVenue map[string]time.Weekday, incomeByPosVenue map[string]float64, estimatedByPos map[int64]float64) {
	// 1. 匹配 broker position：OPEN 腿 venueKey+symbol+positionId → Σ profit / Σ swap / Σ commission
	// D-117 修正：加 positionId 约束，避免同 venue+symbol 跨 position double-counting（D-095 遗留 bug）
	openLegs := filterOpenLegs(legs)
	floating, swapAccrued, commission, matched := matchBrokerLegs(openLegs, brokerSnap, pb.Id)
	// D-146 S5：funding 并入 swapAccrued 口径；D-322 S3b：per-venue 去重——venue 实报
	// （bp.Swap≠0，已计入 swapAccrued）的 venue 跳过，否则兜底计入。D-375 S2：兜底源换 income 实收。
	funding := fundingForLegs(pb.Id, openLegs, brokerSnap, incomeByPosVenue)
	swapAccrued += funding // 口径扩展：swap 快照 + funding 实收
	// D-375 S2：估参考行——独立于 matched||funding 门控（未匹配仓也显示「—」+估行），>0 才填。
	if est := estimatedByPos[pb.Id]; est > 0 {
		pb.SwapEstimatedUsd = &est
	}
	if matched || funding != 0 {
		pb.FloatingPnlUsd = &floating
		pb.SwapAccruedUsd = &swapAccrued
		// D-093（业主授权净到手口径）：net 含出场费估——CEX 腿 taker 费按 app_config
		// 热键计提（透明单列 exit_fee_est_usd，读者可对账 net 差额来源）；MT 腿 0 点差内嵌。
		exitFee := s.exitFeeEstUSD(ctx, pb, openLegs)
		if exitFee != 0 {
			pb.ExitFeeEstUsd = &exitFee
		}
		net := floating + swapAccrued + commission - exitFee
		pb.NetPnlUsd = &net

		// D-480：平仓净估——出口价浮动+实收+已付佣金−退出成本（缺报价→NULL 不猜）。
		if cn, exitCost, ok := s.closeNowNet(ctx, pb, openLegs, brokerSnap, swapAccrued, commission, now); ok {
			pb.CloseNowNetUsd = &cn
			pb.CloseExitCostsUsd = &exitCost
		}

		// D-117→D-391：回本周期「本」口径重定义=入场摩擦成本（点差+手续费+佣金），
		// 开仓即定不随行情漂；剩余洞=本−息差实收；≤0 → 0（已回本）；>0 → 剩余÷当前日息率。
		// 净盈亏列口径不动（回答「现在平仓亏多少」，与回本互补）。
		if carryTypes[pb.Type] {
			if s.econPosFn != nil {
				// D-014 ④：回本列=econ 同源——剩余洞=(Entry+ExitEst−Accrued)÷CarryPerDay。
				// 装配缺账 → 缺省显示（fail-closed 诚实空态，不回落旧口径）。
				if snap, err := s.econPosFn(ctx, pb.Id); err == nil && snap.CarryPerDayUSD > 0 {
					remaining := snap.EntryCostUSD + snap.ExitCostEstUSD - snap.AccruedUSD
					var breakEven float64
					if remaining > 0 {
						breakEven = remaining / snap.CarryPerDayUSD
					}
					pb.BreakEvenDays = &breakEven
				}
			} else {
				holdDays := holdDaysToNow(pb, openLegs, now)
				if holdDays > 0 {
					dailySwap := swapAccrued / holdDays
					if dailySwap > 0 {
						entryCost := s.entryFrictionUSD(ctx, pb, openLegs, brokerSnap)
						remaining := entryCost - swapAccrued
						var breakEven float64
						if remaining > 0 {
							breakEven = remaining / dailySwap
						}
						// remaining ≤ 0 → breakEven=0（已回本）；dailySwap>0 保证除法安全
						pb.BreakEvenDays = &breakEven
					}
					// dailySwap ≤ 0 → 缺省（付息型或不结算，不显示回本周期）
				}
			}
		}
	}

	// 2a. PERP_FUNDING 三列（D-146 S5 微件②）：年化 expected_net_bps 按持有天数折算应得。
	// CEX funding 24/7 计息（8h×3/天，含周末）→ 用 calendar holdDays 非 settleDays（MT5 swap 日历）。
	// expected_net_bps 为年化百分点（5.0=5%，evaluator.go:85-87）→ expected_usd = (bps/100)×notional×(holdDays/365)。
	// 实收复用 swapAccrued（S5 已并 funding 合计）；达成=实收÷应得×100。
	// XASSET_CARRY（D-311 R7②）：perp 腿 funding 同为 24/7 计息年化%口径，复用同款模型——修类型门控不修公式。
	if (pb.Type == "PERP_FUNDING" || pb.Type == "XASSET_CARRY") && pb.ExpectedNetBps > 0 {
		canonical := canonicalFromOppID(pb.OpportunityId)
		notionalUSD := computeEntryNotional(ctx, s, openLegs, canonical)
		if notionalUSD > 0 {
			holdDays := holdDaysToNow(pb, openLegs, now)
			expected := perpFundingExpectedUSD(pb.ExpectedNetBps, notionalUSD, holdDays)
			if expected > 0 {
				pb.ExpectedSwapUsdToDate = &expected
				if swapAccrued > 0 {
					pct := swapAccrued / expected * 100
					pb.AttainmentPct = &pct
				}
			}
		}
	}

	// 2b. SWAPARB/CARRY/FUNDING_HEDGE：日 swap bps × 结算天数（D-118 结算日历感知，周末排除+三倍日）。
	// PERP_FUNDING 不走此路（ExpectedSwapBpsDaily=nil，SWAPARB 专属字段）。
	if carryTypes[pb.Type] && pb.ExpectedSwapBpsDaily != nil && *pb.ExpectedSwapBpsDaily > 0 {
		canonical := canonicalFromOppID(pb.OpportunityId)
		notionalUSD := computeEntryNotional(ctx, s, openLegs, canonical)
		if notionalUSD > 0 {
			openedAt := openedAtTime(pb, openLegs)
			if !openedAt.IsZero() && now.After(openedAt) {
				// D-118：结算日历感知——取双腿 venue 的三倍日（任一腿有三倍日即用）
				tripleDay, hasTriple := pickTripleDay(openLegs, tripleDayByVenue)
				settleDays, tripleDays := settlementDays(openedAt, now, tripleDay, hasTriple)
				if settleDays > 0 {
					// 预期 = dailySwap × (结算天数 + 2×三倍日数)
					// 三倍日 = 3倍 = 1倍基础（已在结算天数里）+ 2倍额外
					dailySwap := *pb.ExpectedSwapBpsDaily / 10000 * notionalUSD
					expected := dailySwap * float64(settleDays+2*tripleDays)
					pb.ExpectedSwapUsdToDate = &expected
					if swapAccrued > 0 && expected > 0 {
						pct := swapAccrued / expected * 100
						pb.AttainmentPct = &pct
					}
				}
			}
		}
	}

	// 3. 收敛型：当前 net bps（实时报价重算，D-111）
	if convergentTypes[pb.Type] {
		if bps, ok := computeCurrentBps(pb, openLegs, s.qbus, s.commissionFor, s.slippageBps); ok {
			pb.CurrentNetBps = &bps
		}
	}

	// 4. D-311 R10：回本列预期值回落——透传 opp.breakeven_days（入场评估保本天数），
	// 前端实测缺省时回落显示「预 X.X 天」（实测优先在展示侧；XASSET 公式含价差冲减可近 0，>=0 即收）。
	// error/行缺失/NULL/负值 → 静默缺省（勿污染主路径）；每仓一次查询（N 小，不批量预取）。
	if pb.OpportunityId != "" && s.store != nil {
		if o, err := s.store.GetOpportunity(ctx, pb.OpportunityId); err == nil && o != nil && o.BreakevenDays != nil && *o.BreakevenDays >= 0 {
			pb.ExpectedBreakevenDays = o.BreakevenDays
		}
	}

	// 5. D-508 S8 前端零信任收编——阈值/抑制/兜底终值全部后端下发（实现=helpers 同文件域）。
	enrichDisplayTerminals(pb, openLegs, now)
}

// entryFrictionUSD D-391 R1：回本列「本」=入场摩擦成本（USD，开仓即定不随行情漂）。
//   - 点差付出：max(0, entrySpreadBps)/10000 × notionalUSD。EntrySpreadBpsFromLegSlice 用成交价
//     （期望点差+双腿滑点已合并，滑点不单列——498 型 −11.56bps 已含 $0.13 滑点）；
//     点差为赚（<0）→ 计 0 且不冲抵手续费。
//   - 手续费逐腿三级互斥（同笔费只计一次）：fee_usd > 快照 commission > 估算。
//     ① l.FeeUSD 非空 → 实值优先，该腿快照 commission 跳过（防双计：D-385 起 CEX 聚合仓
//     bp.Commission 亦映射真费，498 实证快照 −2.18095 与 NULL 估算 $2.1810 双计）；
//     ② CEX 腿 fee NULL 且快照有该腿负 commission → 快照真费（仓位累计费近似入场摩擦——
//     部分平仓后含出场费略高估，出场费也是已付成本，接受近似）；
//     ③ CEX 腿双缺 → notional×XAssetCexFeeBps 估算回退（log 注明）；
//     ④ MT 腿 → max(0,−legCommission)（负=付出才计，返佣正号不反向减）。
//     legCommission 匹配谓词与 matchBrokerLegs 同型（venueKey+symbol+positionId）；
//     同 venue+symbol 多腿共享同一 bp 的既有局限照旧（498 型一腿一 venue 不受影响）。
func (s *Service) entryFrictionUSD(ctx context.Context, pb *v1.Position, openLegs []pgstore.PositionLeg, brokerSnap []*v1.BrokerPosition) float64 {
	canonical := canonicalFromOppID(pb.OpportunityId)
	cost := 0.0
	if bps, ok := pgstore.EntrySpreadBpsFromLegSlice(openLegs); ok && bps > 0 {
		if n := computeEntryNotional(ctx, s, openLegs, canonical); n > 0 {
			cost += bps / 10000 * n
		}
	}
	quoteCcy := notional.QuoteCcy(canonical)
	for _, l := range openLegs {
		if l.FeeUSD != nil {
			cost += *l.FeeUSD
			continue
		}
		legComm := 0.0
		for _, bp := range brokerSnap {
			if bp.VenueKey == l.Venue && bp.Symbol == l.Symbol && bp.PositionId == pb.Id {
				legComm += bp.Commission
			}
		}
		if isCEXVenueKey(l.Venue) {
			if legComm < 0 {
				cost += -legComm // 快照真费优先于估算
				continue
			}
			if s.xassetCexFeeBps > 0 && s.store != nil {
				if ln := s.legNotionalUSD(ctx, l, canonical, quoteCcy); ln > 0 {
					est := ln * s.xassetCexFeeBps / 10000
					log.Printf("[enrich] pos=%d leg %s %s fee_usd NULL → 估算 $%.4f（notional %.2f × %.1fbps）", pb.Id, l.Venue, l.Symbol, est, ln, s.xassetCexFeeBps)
					cost += est
				}
			}
			continue
		}
		cost += math.Max(0, -legComm)
	}
	return cost
}
