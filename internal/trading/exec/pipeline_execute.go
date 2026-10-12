// Package exec 的 pipeline_execute.go 实现 Execute 方法（从 pipeline.go 拆出，保 pipeline.go ≤300 行）。
package exec

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"arb/internal/data/keydomain"
	"arb/internal/platform/safego"
	"arb/internal/store/pgstore"
)

// leverageSetter D-079：perp 腿开仓前置 set-leverage 可选缝（CEXExecutor 实现；
// MT5Executor 不实现=跳过）。target>0 时 pipeline 在 MarginPrecheck 前调用——
// 设败 fail-closed 拒开（发生在建 position 之前=零半腿风险）。
type leverageSetter interface {
	EnsureVenueLeverage(ctx context.Context, symbol string, sellLeg bool, target float64) error
}

// Execute 执行机会（§13.1）。
// opportunityID + requestID 幂等：重复提交返回已有 position。
func (p *Pipeline) Execute(ctx context.Context, opportunityID, requestID, posType string, legs []LegSpec) (*ExecuteResult, error) {
	// 风控⓪：fail-fast 入口守卫（修复⑭ S4）。空 type 撞 positions_type_check 约束、nil legs 无
	// 下单意图——两者均不应落库，在触达任何 DB 写或风控查询前拒绝（生产数据完整性隐患）。
	if posType == "" {
		return nil, ErrEmptyPositionType
	}
	if len(legs) == 0 {
		return nil, ErrEmptyLegs
	}

	// request_id 幂等——重复提交返回已有 position，不新建。
	if existing, hit, err := p.checkRequestIdIdempotency(ctx, requestID); err != nil {
		return nil, err
	} else if hit {
		return existing, nil
	}

	// 风控①：持仓上限（D-058）。MaxOpenPositions>0 才启用；OPEN+OPENING+LEGGED 计数超限拒单。
	// check-then-act 无 DB 级原子性——当前单用户人工确认，接受窗口竞态（全自动才需约束）。
	if p.limits.MaxOpenPositions > 0 {
		n, err := p.store.CountOpenPositions(ctx)
		if err != nil {
			return nil, fmt.Errorf("count open positions: %w", err)
		}
		if n >= p.limits.MaxOpenPositions {
			return nil, ErrMaxOpenPositions
		}
	}

	// 风控②：幂等防重（D-058）。同机会已有未平仓持仓（OPEN/OPENING/LEGGED）拒绝重复执行，
	// 防双击/重试重复建仓；CLOSED/FAILED 可重入（新的执行意图）。requestID 级幂等需新列，暂以
	// opportunityID 级覆盖重复提交的主要危害，留待未来。
	dup, err := p.store.HasOpenPositionByOpportunity(ctx, opportunityID)
	if err != nil {
		return nil, fmt.Errorf("check existing position: %w", err)
	}
	if dup {
		return nil, ErrDuplicateExecution
	}

	// 风控③：下单前预检（D-121）：对每条腿调 executor.Precheck（如果实现了 Prechecker），
	// 任一腿预检失败 → 不建 position 不下单，直接返回错误（防 LEGGED）。
	// gate testnet 部分合约缺失（如 TRX_USDT）→ 不预检则一腿成交一腿失败→LEGGED。
	for _, leg := range legs {
		// 开仓对称面——预检环先于 position 落库与任何下单（天然 all-or-nothing），
		// 腿无可交易行情（休市/断流）→ 拒开不 LEGGED（#464 开仓侧同型前科的对称补面）。
		exe, ok := p.registry.Get(leg.VenueKey)
		if !ok {
			return nil, fmt.Errorf("precheck: no executor for venue %s", leg.VenueKey)
		}
		var prober TradeModeProber
		if tp, ok := exe.(TradeModeProber); ok {
			prober = tp
		}
		if ok, why := LegActionable(ctx, p.store, prober, leg.VenueKey, leg.VenueSymbol, leg.Side, false); !ok {
			return nil, fmt.Errorf("品种 %s@%s 当前无可交易行情（休市或断流），拒开: %s",
				leg.VenueSymbol, leg.VenueKey, why)
		}
		if pc, ok := exe.(Prechecker); ok {
			// Order.Qty 契约=基础币——:perp 腿同 executeLeg 边界换算。
			orderQty, convErr := p.orderQtyFor(leg)
			if convErr != nil {
				return nil, fmt.Errorf("precheck leg %s: %w", leg.VenueKey, convErr)
			}
			if err := pc.Precheck(ctx, Order{
				Symbol: leg.VenueSymbol,
				Side:   leg.Side,
				Qty:    orderQty,
				Venue:  leg.VenueKey,
			}); err != nil {
				return nil, fmt.Errorf("precheck leg %s: %w", leg.VenueKey, err)
			}
		}
		// perp 腿开仓前置杠杆策略化——posType ∈ {XASSET_CARRY, FUNDING_HEDGE}
		// 且 target>0 时先 ensure 到配置值（卖腿 sellLeg=true；MT5 不实现缝=跳过）。
		if p.perpLeverageTarget > 0 && (posType == "XASSET_CARRY" || posType == "FUNDING_HEDGE") && keydomain.Parse(leg.VenueKey).Type == "perp" {
			if ls, ok := exe.(leverageSetter); ok {
				if err := ls.EnsureVenueLeverage(ctx, leg.VenueSymbol, leg.Side == "SELL", p.perpLeverageTarget); err != nil {
					return nil, fmt.Errorf("leverage ensure leg %s: %w", leg.VenueKey, err)
				}
			}
		}
		// venue 原生资金预检（MarginPrechecker，fail-closed）——与 Precheck
		// 同位点同拒开语义：不建 position 不下单，结构上不可能 LEGGED。
		if mp, ok := exe.(MarginPrechecker); ok {
			orderQty, convErr := p.orderQtyFor(leg)
			if convErr != nil {
				return nil, fmt.Errorf("margin precheck leg %s: %w", leg.VenueKey, convErr)
			}
			if err := mp.MarginPrecheck(ctx, Order{
				Symbol: leg.VenueSymbol,
				Side:   leg.Side,
				Qty:    orderQty,
				Venue:  leg.VenueKey,
			}); err != nil {
				return nil, fmt.Errorf("margin precheck leg %s: %w", leg.VenueKey, err)
			}
		}
	}

	// 风控④：D-159 W1a R1 S2.3 SWAPARB 精确组合门禁——真实名义额检查。
	// swapArbComboAllow 非 nil 且 posType=SWAPARB 时，建 position 前校验：
	//   1. (canonical, venueBuy, venueSell) 在 allowlist
	//   2. 双腿名义额 ≤ spec.MaxLongQty/MaxShortQty（>0 才校验）
	//   3. 双腿名义额合计 USD ≤ spec.MaxGrossExposureUSD（>0 才校验）
	// nil/空 = 未接线（gate 层 + dashboard 手动入口兜底）。
	if posType == "SWAPARB" && len(p.swapArbAllowSpecs()) > 0 {
		if err := p.checkSwapArbComboAllow(ctx, opportunityID, legs); err != nil {
			return nil, err
		}
	}

	// XASSET_CARRY 精确组合门禁——零信任显式白名单（空表=全拒，区别于 SWAPARB
	// 的 nil=未接线）。建 position 前校验组合允许 + 双腿名义额/gross 上限。
	if posType == "XASSET_CARRY" {
		if err := p.checkXAssetComboAllow(ctx, opportunityID, legs); err != nil {
			return nil, err
		}
	}
	// canonical 聚簇 cap——建 position 走 advisory 锁事务复查（防同刻双放行）。
	capGuardCanonicals, capGuardVenueMargins, capGuardProposed, err := p.capGuardInputs(ctx, posType, opportunityID, legs)
	if err != nil {
		return nil, err
	}

	// 建 position(OPENING)——type 在创建时即写入（修复⑭ S4：原创建空 type 撞 positions_type_check，
	// 靠调用方事后 UpdatePositionMeta 补 type，空窗期约束已拒）。
	// XASSET/FUNDING_HEDGE 走 advisory 锁+canonical 复查的守卫建库（TOCTOU）。
	newPos := &pgstore.Position{
		OpportunityID: opportunityID,
		Type:          posType,
		Status:        "OPENING",
		RequestID:     requestID,
		DedupKey:      opportunityID, // 当前 dedup_key 业务逻辑=opportunity_id（combo.Key 格式）
		Environment:   p.environment, // D-195-B：从 config 注入
	}
	createFn := func() (int64, error) { return p.store.CreatePosition(ctx, newPos) }
	if len(capGuardCanonicals) > 0 {
		createFn = func() (int64, error) {
			return p.store.CreatePositionWithCapGuard(ctx, newPos, capGuardCanonicals, capGuardVenueMargins, capGuardProposed, p.venueMarginSum)
		}
	}
	posID, err := createFn()
	if err != nil {
		return nil, fmt.Errorf("create position: %w", err)
	}

	// 下单：D-018 阶段a 单腿 maker 序（策略已配置+恰一 CEX perp 腿）启用；
	// mt-first（XASSET/FH 恰一 MT 腿）次之；否则既有双腿并发市价路径。
	results := make([]LegResult, len(legs))
	if plan, makerOK := p.makerPlan(ctx, opportunityID, legs); makerOK {
		if !p.makerBudgetAllow(opportunityID) {
			return nil, fmt.Errorf("opp %s: %w（每小时 ≤%d 次，fail-closed 不回落市价）", opportunityID, ErrMakerBudgetExhausted, p.makerMaxPerHour)
		}
		var merr error
		if results, merr = p.runMakerSequence(ctx, requestID, posID, legs, plan); merr != nil {
			return nil, merr
		}
	} else if p.mtFirstEligible(posType, legs) {
		// MT 腿先行——MT 拒单零 CEX 往返成本（mtapi REQUEST_ERROR 间歇性实证）。
		results = p.runMTFirstSequence(ctx, requestID, posID, legs)
	} else {
		var wg sync.WaitGroup
		for i, leg := range legs {
			wg.Add(1)
			idx, l := i, leg
			safego.Go("pipeline:leg-exec", func() {
				defer wg.Done()
				// client_order_id 确定性编码（attempt_no=1 首次）。
				coid := generateClientOrderID(requestID, posID, int16(idx), 1)
				results[idx] = p.executeLeg(ctx, l, coid)
			})
		}
		wg.Wait()
	}

	// 判定最终状态
	filled := 0
	unknown := 0
	for _, r := range results {
		if r.Status == "FILLED" {
			filled++
		}
		if r.Status == "UNKNOWN" {
			unknown++
		}
	}
	// 腿量匹配守卫——判 OPEN 前全 FILLED 腿有效量（canonical 口径）两两偏差
	// 超容差 → 不判 OPEN，status=LEGGED + critical 告警，落既有 autoFlatten/unwind
	//（腿量不匹配是比 LEGGED 更坏的失败：不匹配的 OPEN 看起来一切正常、裸露差量无告警）。
	qtyMismatch := false
	if filled == len(legs) && unknown == 0 {
		qtyMismatch = p.legQtyMismatch(legs, results)
	}

	var status string
	var isUnknown bool
	switch {
	case unknown > 0:
		// MT 超时→CLOSING+uncertainty_reason 过渡（SSOT I1，不新增 DB 枚举）。
		// 不平仓不假 FAILED——可能已成交，假 FAILED 会触发平仓=反向开新仓。
		status = "CLOSING"
		isUnknown = true
	case filled == len(legs) && !qtyMismatch:
		status = "OPEN"
	case filled == 0:
		status = "FAILED"
	default:
		status = "LEGGED"
	}

	// 回填 legs 到 DB（D-108 A1 fail-closed：腿写库失败 → error+critical 告警，孤儿不可留）
	// client_order_id + attempt 字段填充（提取到 persistOpenLegs 避免 pipeline.go 超限）。
	if err := p.persistOpenLegs(ctx, requestID, posID, legs, results); err != nil {
		return &ExecuteResult{PositionID: posID, Status: "FAILED", Legs: results}, err
	}

	// 更新 position 状态
	if err := p.store.UpdatePositionStatus(ctx, posID, status); err != nil {
		if errors.Is(err, pgstore.ErrTerminalState) {
			log.Printf("[exec] position %d already terminal, skip status=%s (G4)", posID, status)
		} else {
			log.Printf("[exec] position %d status update: %v", posID, err)
		}
	}
	p.markOpenIncident(ctx, posID, status)                                 // D-013 ③：LEGGED/FAILED → legged_unwind
	p.writeOpenNotional(ctx, posType, opportunityID, posID, legs, results) // D-032 R1：XASSET/FH 落 notional_usd=Σ腿名义
	// UNKNOWN 过渡/腿量不匹配告警/LEGGED 收平 → finalizeOpenPostLegs（D-026 R1 拆分，纯搬移）。
	if isUnknown || qtyMismatch || status == "LEGGED" {
		status, err = p.finalizeOpenPostLegs(ctx, posID, legs, results, status, filled, isUnknown, qtyMismatch)
		if err != nil {
			log.Printf("[exec] pos %d finalize: %v", posID, err)
		}
	}
	return &ExecuteResult{
		PositionID: posID,
		Status:     status,
		Legs:       results,
	}, nil
}
