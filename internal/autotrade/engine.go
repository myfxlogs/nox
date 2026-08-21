package autotrade

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"arb/internal/bus"
	"arb/internal/dashboard"
	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// Config 自动交易引擎配置（D-070/D-072）。
type Config struct {
	Enabled      bool
	MaxNotional  float64
	MaxPositions int
	MaxDailyLoss float64
	MinNetBps    float64
	CloseBpsThr  float64       // 收敛型平仓阈值（expected_net_bps <= 此值则平仓）
	PollInterval time.Duration // OPEN 持仓扫描间隔
	NotionalUSD  float64       // 单笔名义额（用于算 qty）

	// D-072 §3.4 硬编码外部化（默认值=现行为，零迁移）
	AllowVenues       map[string]bool    // handleOpportunity venue 白名单 + MR venueA/B
	AllowCanonicals   map[string]bool    // handleOpportunity symbol 白名单
	ExcludeCanonicals map[string]bool    // BRENT/WTI 排除
	MeanRevertThresh  map[string]float64 // MR 阈值 map

	// D-072 §3.3/§4.2 swap/funding 退出参数
	MinNetSwapBps  float64 // discoverSwapCombos 最低净 swap（ARB_AUTOTRADE_MIN_NET_SWAP_BPS，默认 0.2）
	SwapArbExitBps float64 // SWAPARB/CARRY 退出净 swap bps（ARB_SWAPARB_EXIT_NET_BPS，默认 0）
	FundingExitPct float64 // FUNDING_HEDGE/PERP_FUNDING 退出净年化（ARB_FUNDING_EXIT_NET_PCT，默认 0）
}

// Engine 自动交易引擎：三 loop → gate → 执行 → 自动平仓 → 策略淘汰（D-070/D-072）。
type Engine struct {
	cfg      Config
	store    *pgstore.Store
	service  *dashboard.Service
	oppTopic *bus.Topic[pgstore.Opportunity]
	quotes   *bus.QuoteBus // 实时报价（D-070 均值回归平仓用）
	gate     *Gate         // 统一开仓门禁（D-072）

	firstTickDone bool // 启动首跑淘汰标记

	// fundingNeedSent 资金注入提醒节流（D-072）：key=SWAPARB:canonical:venueLong:venueShort → YYYY-MM-DD。
	// 每日每组合至多 1 条（内存，重启重置——实时弹窗语义，不持久化）。
	fundingNeedSent map[string]string
}

// New 创建自动交易引擎。
func New(cfg Config, store *pgstore.Store, service *dashboard.Service, oppTopic *bus.Topic[pgstore.Opportunity], quotes *bus.QuoteBus) *Engine {
	return &Engine{
		cfg:             cfg,
		store:           store,
		service:         service,
		oppTopic:        oppTopic,
		quotes:          quotes,
		gate:            NewGate(store, cfg),
		fundingNeedSent: make(map[string]string),
	}
}

// ValidateStartup 启动 fail-fast 校验（D-072 实施修正②）。
// OpportunityNotionalUSD > AutoTradeMaxNotional → log.Fatal（SWAPARB 不参与）。
func (e *Engine) ValidateStartup() {
	if e.cfg.NotionalUSD > e.cfg.MaxNotional && e.cfg.MaxNotional > 0 {
		log.Fatalf("[autotrade] config fail-fast: OpportunityNotionalUSD $%.0f > AutoTradeMaxNotional $%.0f (fix env or lower ARB_OPP_NOTIONAL_USD)", e.cfg.NotionalUSD, e.cfg.MaxNotional)
	}
}

// Run 启动自动交易引擎（阻塞，ctx 取消时退出）。
func (e *Engine) Run(ctx context.Context) {
	if !e.cfg.Enabled {
		log.Println("[autotrade] disabled (ARB_AUTOTRADE_ENABLED=false)")
		return
	}
	e.ValidateStartup()
	log.Printf("[autotrade] enabled: maxNotional=$%.0f maxPos=%d maxDailyLoss=$%.0f minNetBps=%.1f poll=%s",
		e.cfg.MaxNotional, e.cfg.MaxPositions, e.cfg.MaxDailyLoss, e.cfg.MinNetBps, e.cfg.PollInterval)

	// 1. 机会订阅循环（detector 产生的 CROSS_VENUE 机会）
	go e.oppLoop(ctx)

	// 2. 均值回归扫描循环（D-070：绕过 detector，直接监控价差）
	go e.meanRevertLoop(ctx)

	// 3. swap 套利循环（D-070：持有赚 swap）
	go e.swapArbLoop(ctx)

	// 4. 持仓扫描循环（自动平仓 + 状态机 tick）
	e.positionLoop(ctx)
}

// swapArbLoop D-072 swap 套利：自动发现跨账户 swap 组合，名义额=broker 余额驱动。
func (e *Engine) swapArbLoop(ctx context.Context) {
	if e.quotes == nil {
		return
	}
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.scanSwapArb(ctx)
		}
	}
}

// scanSwapArb 自动扫描所有跨账户 swap 组合，找净 swap 最优的组合开仓。
// D-072：风控由 gate 统一处理，本函数只产 TradeIntent。
func (e *Engine) scanSwapArb(ctx context.Context) {
	combos := e.discoverSwapCombos(ctx)
	if len(combos) == 0 {
		return
	}
	for _, c := range combos {
		e.trySwapArb(ctx, c.canonical, c.vLong, c.vShort, c.netSwapBps)
	}
}

// swapComboCandidate 自动扫描发现的 swap 套利候选组合。
type swapComboCandidate struct {
	canonical, vLong, vShort string
	netSwapBps               float64
}

// discoverSwapCombos 从 facts 表自动扫描所有跨账户 swap 组合。
// D-072：minNetSwapBps 从 env 读取（ARB_AUTOTRADE_MIN_NET_SWAP_BPS，默认 0.2）。
func (e *Engine) discoverSwapCombos(ctx context.Context) []swapComboCandidate {
	longFacts, err := e.store.LatestFacts(ctx, fact.KindSwapLong, "", "")
	if err != nil || len(longFacts) == 0 {
		return nil
	}
	shortFacts, err := e.store.LatestFacts(ctx, fact.KindSwapShort, "", "")
	if err != nil || len(shortFacts) == 0 {
		return nil
	}
	type swapKey struct{ canonical, venue string }
	shortMap := make(map[swapKey]float64)
	for _, f := range shortFacts {
		shortMap[swapKey{f.Symbol, f.Venue}] = f.Value
	}
	var combos []swapComboCandidate
	minNetSwapBps := e.cfg.MinNetSwapBps
	if minNetSwapBps == 0 {
		minNetSwapBps = 0.2
	}
	for _, lf := range longFacts {
		if lf.Value <= 0 {
			continue
		}
		for sfKey, sfVal := range shortMap {
			if sfKey.canonical != lf.Symbol {
				continue
			}
			if sfKey.venue == lf.Venue {
				continue
			}
			netSwap := lf.Value + sfVal
			if netSwap < minNetSwapBps {
				continue
			}
			combos = append(combos, swapComboCandidate{
				canonical:  lf.Symbol,
				vLong:      lf.Venue + ":mt5",
				vShort:     sfKey.venue + ":mt5",
				netSwapBps: netSwap,
			})
		}
	}
	sort.Slice(combos, func(i, j int) bool {
		return combos[i].netSwapBps > combos[j].netSwapBps
	})
	return combos
}

// trySwapArb 尝试开仓单个 swap 套利组合（D-072：名义额=broker 余额驱动，经 gate）。
func (e *Engine) trySwapArb(ctx context.Context, canonical, venueLong, venueShort string, netSwapBps float64) {
	qLong, okL := e.quotes.Latest(canonical, venueLong)
	qShort, okS := e.quotes.Latest(canonical, venueShort)
	if !okL || !okS {
		return
	}
	now := time.Now()
	if now.Sub(qLong.Time) > 120*time.Second || now.Sub(qShort.Time) > 120*time.Second {
		return
	}
	spread := qShort.Bid - qLong.Ask
	spreadBps := spread / qLong.Ask * 10000
	if spreadBps < -3.0 {
		log.Printf("[autotrade] SWAPARB skip %s: spreadBps=%.2f < -3.0", canonical, spreadBps)
		return
	}
	// D-072 §3.3：名义额 = broker 余额驱动（fail-closed 三条件）
	notional, ok, reason := e.service.SwapArbNotional(canonical, venueLong, venueShort)
	if !ok {
		// D-072 资金注入提醒：余额不足 → 发布 FundingNeedNotice 到 topic → 前端弹窗（不入 alerts 表）。
		// 每日每组合至多 1 条（内存节流）；非余额原因（venue 离线/币种不符）仅 log 不弹窗。
		if strings.Contains(reason, "balance") {
			e.maybePublishFundingNeed(canonical, venueLong, venueShort, reason)
		}
		log.Printf("[autotrade] SWAPARB skip %s %s/%s: %s", canonical, venueLong, venueShort, reason)
		return
	}
	qty := notional / qLong.Ask
	dedupKey := fmt.Sprintf("SWAPARB:%s:%s:%s", canonical, venueLong, venueShort)
	intent := TradeIntent{
		Strategy:    "SWAPARB",
		Canonical:   canonical,
		VenueBuy:    venueLong,
		VenueSell:   venueShort,
		Qty:         qty,
		NotionalUSD: notional,
		DedupKey:    dedupKey,
		NetBps:      spreadBps,
		Execute: func(ctx context.Context) (string, string, int64) {
			reqID := fmt.Sprintf("swaparb-%d", time.Now().UnixNano())
			status, msg, posID := e.service.ExecuteMeanRevertWithCap(ctx, canonical, venueLong, venueShort, reqID, qty, spreadBps, notional, "SWAPARB")
			if status != "FAILED" && posID > 0 {
				oppID := fmt.Sprintf("SWAPARB:%s:%s:%s:%d", canonical, venueLong, venueShort, time.Now().UnixMilli())
				_ = e.store.UpdatePositionOppID(ctx, posID, oppID)
			}
			return status, msg, posID
		},
	}
	e.gate.Submit(ctx, intent)
}

// maybePublishFundingNeed 发布资金注入提醒（D-072）：每日每组合至多 1 条（内存节流）。
// NeedUSD 由 SwapArbFundingNeedUSD 侧算（不改 SwapArbNotional 签名）；topic 未接线时 Service.PublishFundingNeed 静默丢弃。
func (e *Engine) maybePublishFundingNeed(canonical, venueLong, venueShort, reason string) {
	key := fmt.Sprintf("SWAPARB:%s:%s:%s", canonical, venueLong, venueShort)
	today := time.Now().Format("2006-01-02")
	if e.fundingNeedSent[key] == today {
		return
	}
	needUSD, _ := e.service.SwapArbFundingNeedUSD(canonical, venueLong, venueShort)
	e.service.PublishFundingNeed(dashboard.FundingNeedNotice{
		Canonical:  canonical,
		VenueLong:  venueLong,
		VenueShort: venueShort,
		Reason:     reason,
		NeedUSD:    needUSD,
		Ts:         time.Now(),
	})
	e.fundingNeedSent[key] = today
}

// meanRevertLoop D-072 均值回归扫描：venue 对与阈值从 env 读取（§3.4 外部化）。
func (e *Engine) meanRevertLoop(ctx context.Context) {
	if e.quotes == nil {
		return
	}
	if len(e.cfg.MeanRevertThresh) == 0 {
		log.Println("[autotrade] meanRevertLoop: no thresholds configured (ARB_MEANREVERT_THRESHOLDS empty)")
		return
	}
	// venue 对从 AllowVenues 提取（前两个）
	venues := make([]string, 0, len(e.cfg.AllowVenues))
	for v := range e.cfg.AllowVenues {
		venues = append(venues, v)
	}
	sort.Strings(venues)
	if len(venues) < 2 {
		log.Println("[autotrade] meanRevertLoop: need >=2 allow venues (ARB_AUTOTRADE_ALLOW_VENUES)")
		return
	}
	venueA, venueB := venues[0], venues[1]
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.scanMeanRevert(ctx, e.cfg.MeanRevertThresh, venueA, venueB)
		}
	}
}

// scanMeanRevert 扫描价差，超阈值则产 intent 走 gate（D-072：风控由 gate 统一）。
func (e *Engine) scanMeanRevert(ctx context.Context, symbols map[string]float64, venueA, venueB string) {
	for canonical, threshold := range symbols {
		qA, okA := e.quotes.Latest(canonical, venueA)
		qB, okB := e.quotes.Latest(canonical, venueB)
		if !okA || !okB {
			continue
		}
		now := time.Now()
		if now.Sub(qA.Time) > 120*time.Second || now.Sub(qB.Time) > 120*time.Second {
			continue
		}
		spreadAB := qB.Bid - qA.Ask
		spreadBA := qA.Bid - qB.Ask
		if spreadAB > threshold {
			bps := spreadAB / qA.Ask * 10000
			e.executeMeanRevert(ctx, canonical, venueA, venueB, bps)
		} else if spreadBA > threshold {
			bps := spreadBA / qB.Ask * 10000
			e.executeMeanRevert(ctx, canonical, venueB, venueA, bps)
		}
	}
}

// executeMeanRevert 产 TradeIntent 走 gate（D-072：不直连执行）。
func (e *Engine) executeMeanRevert(ctx context.Context, canonical, venueBuy, venueSell string, openBps float64) {
	qbuy, ok := e.quotes.Latest(canonical, venueBuy)
	if !ok {
		return
	}
	qty := e.cfg.NotionalUSD / qbuy.Ask
	dedupKey := fmt.Sprintf("MEANREVERT:%s:%s:%s", canonical, venueBuy, venueSell)
	intent := TradeIntent{
		Strategy:    "MEANREVERT",
		Canonical:   canonical,
		VenueBuy:    venueBuy,
		VenueSell:   venueSell,
		Qty:         qty,
		NotionalUSD: e.cfg.NotionalUSD,
		DedupKey:    dedupKey,
		NetBps:      openBps,
		Execute: func(ctx context.Context) (string, string, int64) {
			reqID := fmt.Sprintf("mr-%d", time.Now().UnixNano())
			return e.service.ExecuteMeanRevert(ctx, canonical, venueBuy, venueSell, reqID, qty, openBps)
		},
	}
	e.gate.Submit(ctx, intent)
}

// oppLoop 订阅机会事件 → 过滤 → 产 intent 走 gate（D-072：风控由 gate 统一）。
func (e *Engine) oppLoop(ctx context.Context) {
	ch, unsub := e.oppTopic.Subscribe()
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return
		case opp, ok := <-ch:
			if !ok {
				return
			}
			log.Printf("[autotrade] recv opp=%s type=%s netBps=%.2f buy=%s sell=%s", opp.ID, opp.Type, opp.NetBps, opp.VenueBuy, opp.VenueSell)
			e.handleOpportunity(ctx, opp)
		}
	}
}

// isCEXVenue 判断 venue 是否为 CEX（D-070：业主指令「放弃 CEX，全功 CFD」）。
func isCEXVenue(venue string) bool {
	return !strings.Contains(venue, ":mt5") && !strings.Contains(venue, ":mt4")
}

// handleOpportunity 过滤机会 → 产 intent 走 gate（D-072：风控由 gate 统一）。
func (e *Engine) handleOpportunity(ctx context.Context, opp pgstore.Opportunity) {
	// D-070：业主指令「放弃 CEX，全功 CFD」
	if isCEXVenue(opp.VenueBuy) || isCEXVenue(opp.VenueSell) {
		return
	}
	// D-072 §3.4：排除品种从 env 读取
	if e.cfg.ExcludeCanonicals[opp.Canonical] {
		return
	}
	// D-072 §3.4：venue 白名单从 env 读取
	if !e.cfg.AllowVenues[opp.VenueBuy] || !e.cfg.AllowVenues[opp.VenueSell] {
		return
	}
	// D-072 §3.4：symbol 白名单从 env 读取
	if len(e.cfg.AllowCanonicals) > 0 && !e.cfg.AllowCanonicals[opp.Canonical] {
		return
	}
	// 门槛：净收益
	if opp.NetBps < e.cfg.MinNetBps {
		return
	}
	log.Printf("[autotrade] pass filter opp=%s type=%s netBps=%.2f", opp.ID, opp.Type, opp.NetBps)

	qty := e.cfg.NotionalUSD / opp.PriceBuy
	if qty <= 0 {
		return
	}
	intent := TradeIntent{
		Strategy:    opp.Type,
		Canonical:   opp.Canonical,
		VenueBuy:    opp.VenueBuy,
		VenueSell:   opp.VenueSell,
		Qty:         qty,
		NotionalUSD: e.cfg.NotionalUSD,
		DedupKey:    opp.ID,
		NetBps:      opp.NetBps,
		Execute: func(ctx context.Context) (string, string, int64) {
			reqID := fmt.Sprintf("autotrade-%d-%s", time.Now().UnixMilli(), opp.ID)
			return e.service.ExecuteOpportunityInternal(ctx, opp.ID, reqID, qty)
		},
	}
	e.gate.Submit(ctx, intent)
}

// positionLoop 定期扫描 OPEN 持仓 → 按 type 分派退出 + 状态机 tick（D-072）。
func (e *Engine) positionLoop(ctx context.Context) {
	ticker := time.NewTicker(e.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.scanAndClose(ctx)
			e.stateMachineTick(ctx)
		}
	}
}

// scanAndClose 扫描 OPEN 持仓，按 type 分派退出条件（D-072 §4.2）。
func (e *Engine) scanAndClose(ctx context.Context) {
	positions, err := e.store.ListPositions(ctx, "OPEN", 50, 0)
	if err != nil {
		log.Printf("[autotrade] list open positions: %v", err)
		return
	}
	for _, p := range positions {
		shouldClose, reason := e.checkExitCondition(ctx, &p)
		if !shouldClose {
			continue
		}
		log.Printf("[autotrade] auto-close pos=%d type=%s: %s", p.ID, p.Type, reason)
		if err := e.service.AutoClosePosition(ctx, p.ID); err != nil {
			log.Printf("[autotrade] auto-close pos=%d failed: %v", p.ID, err)
			continue
		}
		e.updateStrategyStat(ctx, &p)
	}
}

// checkExitCondition 按 type 分派退出条件（D-072 §4.2）。
// 返回 (shouldClose, reason)。
func (e *Engine) checkExitCondition(ctx context.Context, p *pgstore.Position) (bool, string) {
	switch p.Type {
	case "CROSS_VENUE", "MEANREVERT":
		// 收敛型：currentNetBps ≤ CloseBpsThr
		currentBps, ok := e.currentNetBps(p)
		if !ok {
			return false, ""
		}
		if currentBps <= e.cfg.CloseBpsThr {
			return true, fmt.Sprintf("converged currentBps=%.2f <= %.1f", currentBps, e.cfg.CloseBpsThr)
		}
		return false, ""
	case "SWAPARB", "CARRY":
		// 净 swap ≤ SwapArbExitBps（默认 0）→ 平仓
		netSwap, ok := e.recalcNetSwap(ctx, p)
		if !ok {
			return false, ""
		}
		if netSwap <= e.cfg.SwapArbExitBps {
			return true, fmt.Sprintf("net swap %.3f bps/day <= %.1f", netSwap, e.cfg.SwapArbExitBps)
		}
		return false, ""
	case "FUNDING_HEDGE", "PERP_FUNDING":
		// 净 funding 年化 ≤ FundingExitPct（默认 0）→ 平仓
		netFunding, ok := e.recalcNetFunding(ctx, p)
		if !ok {
			return false, ""
		}
		if netFunding <= e.cfg.FundingExitPct {
			return true, fmt.Sprintf("net funding %.2f%% <= %.1f%%", netFunding, e.cfg.FundingExitPct)
		}
		return false, ""
	}
	return false, ""
}

// recalcNetSwap 重算净 swap（D-072 §4.2，facts 驱动 + 48h 新鲜度守卫）。
func (e *Engine) recalcNetSwap(ctx context.Context, p *pgstore.Position) (float64, bool) {
	canonical := canonicalFromOppID(p.OpportunityID)
	if canonical == "" {
		return 0, false
	}
	longFacts, err := e.store.LatestFacts(ctx, fact.KindSwapLong, "", canonical)
	if err != nil || len(longFacts) == 0 {
		return 0, false
	}
	shortFacts, err := e.store.LatestFacts(ctx, fact.KindSwapShort, "", canonical)
	if err != nil || len(shortFacts) == 0 {
		return 0, false
	}
	// 48h 新鲜度守卫
	if time.Since(longFacts[0].Ts) > 48*time.Hour || time.Since(shortFacts[0].Ts) > 48*time.Hour {
		log.Printf("[autotrade] skip exit pos=%d: swap facts stale >48h", p.ID)
		return 0, false
	}
	// 解析 legs 取 venue
	legs, err := e.store.ListPositionLegs(ctx, p.ID)
	if err != nil {
		return 0, false
	}
	var buyVenue, sellVenue string
	for i := range legs {
		if legs[i].Phase == "OPEN" {
			if legs[i].Side == "BUY" {
				buyVenue = legs[i].Venue
			} else {
				sellVenue = legs[i].Venue
			}
		}
	}
	if buyVenue == "" || sellVenue == "" {
		return 0, false
	}
	var swapLong, swapShort float64
	for _, f := range longFacts {
		if f.Venue == strings.Split(buyVenue, ":")[0] {
			swapLong = f.Value
		}
	}
	for _, f := range shortFacts {
		if f.Venue == strings.Split(sellVenue, ":")[0] {
			swapShort = f.Value
		}
	}
	return swapLong + swapShort, true
}

// recalcNetFunding 重算净 funding 年化（D-072 §4.2，facts 驱动 + 48h 新鲜度守卫）。
func (e *Engine) recalcNetFunding(ctx context.Context, p *pgstore.Position) (float64, bool) {
	canonical := canonicalFromOppID(p.OpportunityID)
	if canonical == "" {
		return 0, false
	}
	fundingFacts, err := e.store.LatestFacts(ctx, fact.KindFunding, "", canonical)
	if err != nil || len(fundingFacts) == 0 {
		return 0, false
	}
	if time.Since(fundingFacts[0].Ts) > 48*time.Hour {
		log.Printf("[autotrade] skip exit pos=%d: funding facts stale >48h", p.ID)
		return 0, false
	}
	legs, err := e.store.ListPositionLegs(ctx, p.ID)
	if err != nil {
		return 0, false
	}
	var buyVenue, sellVenue string
	for i := range legs {
		if legs[i].Phase == "OPEN" {
			if legs[i].Side == "BUY" {
				buyVenue = legs[i].Venue
			} else {
				sellVenue = legs[i].Venue
			}
		}
	}
	if buyVenue == "" || sellVenue == "" {
		return 0, false
	}
	var longFunding, shortFunding float64
	for _, f := range fundingFacts {
		if f.Venue == strings.Split(buyVenue, ":")[0] {
			longFunding = f.Value
		}
		if f.Venue == strings.Split(sellVenue, ":")[0] {
			shortFunding = f.Value
		}
	}
	return longFunding - shortFunding, true
}

// canonicalFromOppID 从 opportunity_id 解析 canonical（格式 PREFIX:SYMBOL:...）。
func canonicalFromOppID(oppID string) string {
	parts := strings.Split(oppID, ":")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// stateMachineTick 状态机每 tick 检查（D-072 §3.2 + §4.3 首跑淘汰）。
func (e *Engine) stateMachineTick(ctx context.Context) {
	// 首跑淘汰：回填后立即对所有策略跑一次淘汰判定
	if !e.firstTickDone {
		e.firstTickDone = true
		e.runStartupElimination(ctx)
	}

	// 读取当前状态
	stateRow, err := e.store.GetAutotradeState(ctx)
	if err != nil {
		return
	}

	// 跨日恢复 + 全策略禁用检查
	dailyPnl, _ := e.store.DailyRealizedPnl(ctx)
	allDisabled := e.checkAllStrategiesDisabled(ctx)
	utcNow := time.Now().UTC().Truncate(24 * time.Hour)

	next, reason := nextState(stateRow.State, dailyPnl, e.cfg.MaxDailyLoss, allDisabled, utcNow, stateRow.HaltedOn)
	if next != stateRow.State {
		var haltedOn *time.Time
		if next == StateDailyLossHalt {
			t := utcNow
			haltedOn = &t
		}
		_ = e.store.SetAutotradeState(ctx, next, reason, haltedOn)
		log.Printf("[autotrade] state %s → %s: %s", stateRow.State, next, reason)
	}
}

// checkAllStrategiesDisabled 检查全部策略是否被禁用。
func (e *Engine) checkAllStrategiesDisabled(ctx context.Context) bool {
	strategies := []string{"CROSS_VENUE", "MEANREVERT", "SWAPARB", "CARRY", "FUNDING_HEDGE", "PERP_FUNDING"}
	for _, s := range strategies {
		disabled, err := e.store.IsStrategyDisabled(ctx, s)
		if err == nil && !disabled {
			return false
		}
	}
	return true
}

// runStartupElimination 启动首跑淘汰判定（D-072 §4.3，回填后立即生效）。
func (e *Engine) runStartupElimination(ctx context.Context) {
	strategies := []string{"CROSS_VENUE", "MEANREVERT", "SWAPARB", "CARRY", "FUNDING_HEDGE", "PERP_FUNDING"}
	for _, s := range strategies {
		stat, err := e.store.GetStrategyStat(ctx, s)
		if err != nil {
			continue
		}
		disable, reason := evaluateElimination(EliminationInput{
			TotalTrades: stat.TotalTrades,
			WinRate:     stat.WinRate,
			AvgPnl:      stat.AvgPnl,
			Disabled:    stat.Disabled,
		})
		if disable {
			if err := e.store.DisableStrategy(ctx, s, reason); err != nil {
				log.Printf("[autotrade] startup elimination disable %s: %v", s, err)
			} else {
				log.Printf("[autotrade] STARTUP ELIMINATION: %s disabled: %s", s, reason)
			}
		}
	}
}

// currentNetBps 实时计算持仓的当前净 bps（收敛型平仓用）。
func (e *Engine) currentNetBps(p *pgstore.Position) (float64, bool) {
	if e.quotes == nil {
		return p.ExpectedNetBps, true
	}
	canonical := canonicalFromOppID(p.OpportunityID)
	if canonical == "" {
		return p.ExpectedNetBps, true
	}
	legs, err := e.store.ListPositionLegs(context.Background(), p.ID)
	if err != nil {
		return 0, false
	}
	var buyLeg, sellLeg *pgstore.PositionLeg
	for i := range legs {
		if legs[i].Phase == "OPEN" {
			if legs[i].Side == "BUY" {
				buyLeg = &legs[i]
			} else {
				sellLeg = &legs[i]
			}
		}
	}
	if buyLeg == nil || sellLeg == nil {
		return 0, false
	}
	buyClose, ok1 := e.quotes.Latest(canonical, buyLeg.Venue)
	sellClose, ok2 := e.quotes.Latest(canonical, sellLeg.Venue)
	if !ok1 || !ok2 {
		return 0, false
	}
	spread := buyClose.Bid - sellClose.Ask
	if spread <= 0 {
		return 0, true
	}
	refPrice := buyClose.Ask
	if buyLeg.PriceActual != nil && *buyLeg.PriceActual > 0 {
		refPrice = *buyLeg.PriceActual
	}
	return spread / refPrice * 10000, true
}

// updateStrategyStat 平仓后更新策略统计 + 检查淘汰（D-072：全 type，含息差型）。
func (e *Engine) updateStrategyStat(ctx context.Context, p *pgstore.Position) {
	if p.RealizedPnl == nil {
		return
	}
	pnl := *p.RealizedPnl
	if err := e.store.UpdateStrategyStat(ctx, p.Type, pnl); err != nil {
		log.Printf("[autotrade] update strategy stat type=%s: %v", p.Type, err)
		return
	}
	stat, err := e.store.GetStrategyStat(ctx, p.Type)
	if err != nil {
		log.Printf("[autotrade] get strategy stat: %v", err)
		return
	}
	disable, reason := evaluateElimination(EliminationInput{
		TotalTrades: stat.TotalTrades,
		WinRate:     stat.WinRate,
		AvgPnl:      stat.AvgPnl,
		Disabled:    stat.Disabled,
	})
	if disable {
		if err := e.store.DisableStrategy(ctx, p.Type, reason); err != nil {
			log.Printf("[autotrade] disable strategy: %v", err)
		} else {
			log.Printf("[autotrade] STRATEGY DISABLED type=%s: %s", p.Type, reason)
		}
	}
}
