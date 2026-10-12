// pipeline_maker.go —— D-018 阶段a：单腿 maker 执行序（执行器层）。
// 设计（盈利闭环审计.md D-018 行）：maker 只挂一条腿（CEX perp 腿），另一腿在首腿成交
// 回报后立即 taker；首腿超时 T 未成交 → 撤单、零持仓（全腿 FAILED→position FAILED，零敞口）；
// 不 chase（挂单即拒/撤=attempt 终态，重挂=引擎新 Execute）；每 opp 每小时 ≤N 次 attempt。
// 适格组合=双腿中含 CEX perp 腿：恰一条（MT×perp=XASSET_CARRY / spot×perp=FUNDING_HEDGE）
// 即该腿 maker；双 perp（SWAPARB）后段起启用——funding 更优一侧挂 maker（swapArbMakerIdx）。
// 配置键缺（timeout/maxPerHour 任一 ≤0）=fail-closed maker 停用。既有 LEGGED unwind
// 链路（autoFlattenLEGGED/UNKNOWN CLOSING）原样复用，零新路径。
package exec

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"
	"nox/internal/data/keydomain"
	"nox/internal/platform/safego"
)

// ErrMakerBudgetExhausted D-018：每 opp 每小时 maker attempt 预算用尽（fail-closed 拒执行，
// 不回落市价——市价=放弃 maker 费率经济性，须引擎显式决策新 attempt）。
var ErrMakerBudgetExhausted = errors.New("maker attempt budget exhausted for opportunity")

// PostOnlyPlacer D-018 阶段a：post-only 执行能力接口（CEXExecutor 实现；测试 fake 注入）。
// 执行器不实现此接口 → makerPlan 不适格，走既有市价路径。
type PostOnlyPlacer interface {
	PlacePostOnly(ctx context.Context, o Order, limitPrice float64) (string, error)
	AwaitPostOnlyFill(ctx context.Context, o Order, orderID string) (Fill, bool, error)
	PostOnlyCancel(ctx context.Context, o Order, orderID string) (Fill, error)
}

// makerPlan 单腿 maker 序计划（makerIdx=post-only 腿下标）。
type makerPlan struct{ makerIdx int }

// makerPlan 判定：策略已配置（fail-closed）+ 双腿 + ≥1 CEX perp 腿。
// 恰一条 perp 腿 → 该腿 maker；双 perp（SWAPARB 形态）→ funding 更优一侧挂 maker
// （D-018 后段②；funding fact 缺 → ok=false 走既有市价路径，保守不猜）。
// 执行器是否实现 PostOnlyPlacer 在 executeMakerLeg 兜底（不适格=腿 FAILED，此处不查
// registry——预检环已证明 executor 存在）。
func (p *Pipeline) makerPlan(ctx context.Context, opportunityID string, legs []LegSpec) (makerPlan, bool) {
	if p.makerTimeout <= 0 || p.makerMaxPerHour <= 0 {
		return makerPlan{}, false // 配置缺=fail-closed（设计钉死）
	}
	if len(legs) != 2 {
		return makerPlan{}, false
	}
	mi := -1
	perpCount := 0
	for i, l := range legs {
		if keydomain.IsType(l.VenueKey, "perp") {
			mi = i
			perpCount++
		}
	}
	if mi < 0 {
		return makerPlan{}, false
	}
	if perpCount == 2 {
		var ok bool
		if mi, ok = p.swapArbMakerIdx(ctx, opportunityID, legs); !ok {
			return makerPlan{}, false
		}
	}
	return makerPlan{makerIdx: mi}, true
}

// swapArbMakerIdx D-018 后段②：双 perp（SWAPARB 形态）maker 腿=持有净 funding 收入
// 更高的一侧（SELL 腿收 +rate / BUY 腿付 −rate，与 autotrade.xassetLegCarryAnnual 同口径）。
// 买腿 canonical=机会 canonical，卖腿=canonical_sell（缺→同 canonical，SWAPARB 常形）。
// 机会查败/任一侧 funding fact 缺 → ok=false（回落既有双腿市价路径，非拒执行）。
func (p *Pipeline) swapArbMakerIdx(ctx context.Context, opportunityID string, legs []LegSpec) (int, bool) {
	buyCanonical := canonicalFromOppID(opportunityID)
	if buyCanonical == "" || p.store == nil {
		return -1, false
	}
	sellCanonical := buyCanonical
	opp, err := p.store.GetOpportunity(ctx, opportunityID)
	if err != nil {
		return -1, false
	}
	if opp != nil && opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
		sellCanonical = *opp.CanonicalSell
	}
	best, bestCarry := -1, 0.0
	for i, l := range legs {
		canonical := buyCanonical
		if l.Side != "BUY" {
			canonical = sellCanonical
		}
		rate, ok := p.fundingRate(ctx, keydomain.BareOf(l.VenueKey), canonical)
		if !ok {
			return -1, false
		}
		carry := rate
		if l.Side == "BUY" {
			carry = -rate
		}
		if best < 0 || carry > bestCarry {
			best, bestCarry = i, carry
		}
	}
	return best, best >= 0
}

// fundingRate 取 (bareVenue, canonical) 最新 funding 年化 %（与 autotrade
// fundingRateForVenue 同读口径：LatestFacts venue="" 全 venue 后按 bare 匹配）。
func (p *Pipeline) fundingRate(ctx context.Context, bareVenue, canonical string) (float64, bool) {
	facts, err := p.store.LatestFacts(ctx, fact.KindFunding, "", canonical)
	if err != nil {
		return 0, false
	}
	for _, f := range facts {
		if f.Venue == bareVenue {
			return f.Value, true
		}
	}
	return 0, false
}

// makerBudgetAllow 每 opp 每小时 attempt 预算（进程内；重启复位=预算从严重置，可接受）。
func (p *Pipeline) makerBudgetAllow(opportunityID string) bool {
	p.makerBudgetMu.Lock()
	defer p.makerBudgetMu.Unlock()
	now := time.Now()
	kept := p.makerBudget[opportunityID][:0]
	for _, ts := range p.makerBudget[opportunityID] {
		if now.Sub(ts) < time.Hour {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= p.makerMaxPerHour {
		p.makerBudget[opportunityID] = kept
		return false
	}
	p.makerBudget[opportunityID] = append(kept, now)
	return true
}

// runMakerSequence 单腿 maker 序：首腿 post-only（超时 T 撤单）→ 成交回报后次腿 taker。
// 返回与双腿并发路径同形的 results（共享后续判定/落库/LEGGED unwind）。
// 首腿 UNKNOWN（撤单失败/查单链路错）→ 原样返回，既有 UNKNOWN CLOSING 链接手。
func (p *Pipeline) runMakerSequence(ctx context.Context, requestID string, posID int64, legs []LegSpec, plan makerPlan) ([]LegResult, error) {
	results := make([]LegResult, len(legs))
	for i := range results {
		// 未发腿显式 PENDING（未尝试语义；空串撞 position_legs_status_check——
		// D-018「超时零持仓」次腿零触达须留审计行）。
		results[i] = LegResult{VenueKey: legs[i].VenueKey, Status: "PENDING"}
	}
	mi := plan.makerIdx
	coid := generateClientOrderID(requestID, posID, int16(mi), 1)
	results[mi] = p.executeMakerLeg(ctx, legs[mi], coid, p.makerTimeout)
	if results[mi].Status != "FILLED" {
		// 未成交（FAILED=干净撤/挂单即拒）或 UNKNOWN（撤单失败）——次腿不发：
		// FAILED 全腿→position FAILED 零敞口（设计「超时零持仓」）；UNKNOWN→既有 CLOSING 链。
		return results, nil
	}
	// 次腿 LegSpec.Qty 按首腿实际成交量等比缩放
	//（ratio=首腿实际成交 base 量/请求 base 量；全额成交 ratio=1=原量；
	// 对 LegSpec 单位无感——同仓两腿同比例）。缩放后经 executeLeg→orderQtyFor/
	// venue 步长与最小量取整：venue 拒（低于最小量）→ 腿 FAILED→共享判定落
	// LEGGED→既有 unwind 收口（不发市价单的等价终态）。
	ratio := 1.0
	if reqBase, err := p.orderQtyFor(legs[mi]); err == nil && reqBase > 0 && results[mi].Qty < reqBase {
		ratio = results[mi].Qty / reqBase
		log.Printf("[exec] maker leg %s: 部分成交 %.6f/%.6f → 次腿等比缩放 x%.4f (D-026)", legs[mi].VenueKey, results[mi].Qty, reqBase, ratio)
	}
	// 首腿成交回报 → 次腿 taker（立即，裸腿窗口=今日并行发单同量级）。
	var wg sync.WaitGroup
	for i, leg := range legs {
		if i == mi {
			continue
		}
		scaled := leg
		scaled.Qty = leg.Qty * ratio
		if scaled.Qty <= 0 {
			// 缩放为零（理论不可达：fill.Qty>0 且请求>0）——防御性不发，留 PENDING 审计。
			log.Printf("[exec] maker 次腿 %s 缩放为零 → 不发（PENDING 审计）", leg.VenueKey)
			continue
		}
		wg.Add(1)
		idx, l := i, scaled
		safego.Go("pipeline:maker-taker-leg", func() {
			defer wg.Done()
			coid2 := generateClientOrderID(requestID, posID, int16(idx), 1)
			results[idx] = p.executeLeg(ctx, l, coid2)
		})
	}
	wg.Wait()
	return results, nil
}

// executeMakerLeg 首腿 post-only 生命周期：挂单→等成交（≤T）→撤单终查。
// 只挂不追：挂单即拒（会吃单）=attempt 终态 FAILED；超时干净撤=FAILED（确定未成交）；
// 撤前已成交部分=FILLED（部分量，次腿不发→共享判定落 LEGGED→既有 unwind 收口）；
// 撤单失败/查单错=UNKNOWN（订单状态未知，禁假 FAILED——D-414 同源教训）。
func (p *Pipeline) executeMakerLeg(ctx context.Context, leg LegSpec, clientOrderID string, timeout time.Duration) LegResult {
	fail := func(status string, ordID string) LegResult {
		return LegResult{VenueKey: leg.VenueKey, OrderID: ordID, Status: status}
	}
	exe, ok := p.registry.Get(leg.VenueKey)
	if !ok {
		return fail("FAILED", "")
	}
	po, ok := exe.(PostOnlyPlacer)
	if !ok {
		log.Printf("[exec] maker leg %s: executor 未实现 PostOnlyPlacer → FAILED", leg.VenueKey)
		return fail("FAILED", "")
	}
	orderQty, err := p.orderQtyFor(leg)
	if err != nil {
		log.Printf("[exec] maker leg %s %s %s: %v", leg.VenueKey, leg.Side, leg.VenueSymbol, err)
		return fail("FAILED", "")
	}
	px, ok := p.makerPassivePrice(leg)
	if !ok {
		log.Printf("[exec] maker leg %s %s: 报价缺→挂价不可得 fail-closed FAILED", leg.VenueKey, leg.VenueSymbol)
		return fail("FAILED", "")
	}
	ord := Order{Symbol: leg.VenueSymbol, Side: leg.Side, Qty: orderQty, Venue: leg.VenueKey, ClientOrderID: clientOrderID}
	ordID, err := po.PlacePostOnly(ctx, ord, px)
	if err != nil {
		log.Printf("[exec] maker leg %s %s %s: 挂单即拒（不 chase）→ FAILED: %v", leg.VenueKey, leg.Side, leg.VenueSymbol, err)
		return fail("FAILED", "")
	}
	// 等待=事件驱动（私有 WS 订单帧→cache 订阅扇出唤醒→快照终判零 REST），
	// 超时 T 撤单终查一次 REST。
	mctx, cancel := context.WithTimeout(ctx, timeout)
	fill, filled, err := po.AwaitPostOnlyFill(mctx, ord, ordID)
	cancel()
	if err != nil {
		// 查单链路错=订单状态未知 → UNKNOWN（禁假 FAILED——假未成交会触发收编平另一腿）。
		log.Printf("[exec] maker leg %s: await 查单错 order=%s → UNKNOWN: %v", leg.VenueKey, ordID, err)
		return fail("UNKNOWN", ordID)
	}
	if filled {
		return LegResult{VenueKey: leg.VenueKey, OrderID: ordID, Price: fill.Price, Qty: fill.Qty,
			Status: "FILLED", FeeUSD: fill.FeeUSD, FillKind: "maker"}
	}
	// 超时 T 到（或 await 期间被撤）→ 撤单终查（脱离 Execute ctx——收尾不随主 ctx 中断）。
	cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer ccancel()
	cfill, cerr := po.PostOnlyCancel(cctx, ord, ordID)
	if cerr != nil {
		log.Printf("[exec] maker leg %s: 撤单失败 order=%s → UNKNOWN: %v", leg.VenueKey, ordID, cerr)
		return fail("UNKNOWN", ordID)
	}
	if cfill.Qty > 0 {
		// 撤前已成交部分=FILLED 部分量——次腿不发，共享判定落 LEGGED → 既有 unwind 收口。
		log.Printf("[exec] maker leg %s: 超时撤单带部分成交 %.6f → FILLED（LEGGED unwind 链接手）", leg.VenueKey, cfill.Qty)
		return LegResult{VenueKey: leg.VenueKey, OrderID: ordID, Price: cfill.Price, Qty: cfill.Qty,
			Status: "FILLED", FeeUSD: cfill.FeeUSD, FillKind: "maker"}
	}
	return fail("FAILED", ordID) // 干净撤=确定未成交（零敞口）
}

// makerPassivePrice maker 挂价：qbus 最新报价取对手价（BUY→bid 贴买一 / SELL→ask 贴卖一，
// post-only 不吃单的最激进挂价=最快成交概率）。报价缺/单边缺=fail-closed false（不追价）。
//
//	quote 匹配 VenueKey+VenueSymbol（LegSpec 无 canonical，AllLatest 线扫——每 opp 一单，量级无害）。
func (p *Pipeline) makerPassivePrice(leg LegSpec) (float64, bool) {
	if p.qbus == nil {
		return 0, false
	}
	for _, q := range p.qbus.AllLatest() {
		if q.VenueKey() != leg.VenueKey || q.VenueSymbol != leg.VenueSymbol {
			continue
		}
		if leg.Side == "BUY" && q.Bid > 0 {
			return q.Bid, true
		}
		if leg.Side == "SELL" && q.Ask > 0 {
			return q.Ask, true
		}
		return 0, false
	}
	return 0, false
}

// 编译期锚：bus.Quote 依赖（makerPassivePrice 消费）未被裁剪。
var _ = bus.Quote{}
