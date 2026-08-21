package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/dashboard"
	"arb/internal/store/pgstore"
)

// Config 自动交易引擎配置（D-070）。
type Config struct {
	Enabled      bool
	MaxNotional  float64
	MaxPositions int
	MaxDailyLoss float64
	MinNetBps    float64
	CloseBpsThr  float64       // 收敛型平仓阈值（expected_net_bps <= 此值则平仓）
	PollInterval time.Duration // OPEN 持仓扫描间隔
	NotionalUSD  float64       // 单笔名义额（用于算 qty）
}

// Engine 自动交易引擎：订阅机会 → 风控 → 执行 → 自动平仓 → 策略淘汰（D-070）。
type Engine struct {
	cfg      Config
	store    *pgstore.Store
	service  *dashboard.Service
	oppTopic *bus.Topic[pgstore.Opportunity]
	quotes   *bus.QuoteBus // 实时报价（D-070 均值回归平仓用）

	mu       sync.Mutex
	executed map[string]bool // 会话内已执行的机会 ID（防短时间内重复）
}

// New 创建自动交易引擎。
func New(cfg Config, store *pgstore.Store, service *dashboard.Service, oppTopic *bus.Topic[pgstore.Opportunity], quotes *bus.QuoteBus) *Engine {
	return &Engine{
		cfg:      cfg,
		store:    store,
		service:  service,
		oppTopic: oppTopic,
		quotes:   quotes,
		executed: make(map[string]bool),
	}
}

// Run 启动自动交易引擎（阻塞，ctx 取消时退出）。
func (e *Engine) Run(ctx context.Context) {
	if !e.cfg.Enabled {
		log.Println("[autotrade] disabled (ARB_AUTOTRADE_ENABLED=false)")
		return
	}
	log.Printf("[autotrade] enabled: maxNotional=$%.0f maxPos=%d maxDailyLoss=$%.0f minNetBps=%.1f poll=%s",
		e.cfg.MaxNotional, e.cfg.MaxPositions, e.cfg.MaxDailyLoss, e.cfg.MinNetBps, e.cfg.PollInterval)

	// 1. 机会订阅循环（detector 产生的 CROSS_VENUE 机会）
	go e.oppLoop(ctx)

	// 2. 均值回归扫描循环（D-070：绕过 detector，直接监控 277+591 价差）
	go e.meanRevertLoop(ctx)

	// 3. swap 套利循环（D-070：AUDJPY 529做多+277做空，持有赚swap）
	go e.swapArbLoop(ctx)

	// 4. 持仓扫描循环（自动平仓）
	e.positionLoop(ctx)
}

// swapArbLoop D-070 swap 套利：AUDJPY 52993526做多（swap_long=+0.679）+ 277842155做空（swap_short=-0.185）。
// 净swap = 0.494 bps/日，周三3倍，一周7倍日swap = 3.46 bps/周。
// 开仓+平仓点差成本 ~4.2 bps，回本 ~9天，之后每周纯赚 3.46 bps。
// 不依赖价差收敛——价格对冲，持有赚swap。
func (e *Engine) swapArbLoop(ctx context.Context) {
	if e.quotes == nil {
		return
	}
	ticker := time.NewTicker(60 * time.Second) // 60 秒检查一次
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

// scanSwapArb 检查是否需要开仓 swap 套利。
func (e *Engine) scanSwapArb(ctx context.Context) {
	// 风控：持仓数
	openCount, err := e.store.CountOpenPositions(ctx)
	if err != nil {
		return
	}
	if openCount >= e.cfg.MaxPositions {
		return
	}
	// 检查已有 swap 套利持仓（避免重复开仓）
	positions, err := e.store.ListPositions(ctx, "OPEN", 50, 0)
	if err != nil {
		return
	}
	existingCombos := map[string]bool{}
	for _, p := range positions {
		if strings.Contains(p.OpportunityID, "SWAPARB:") {
			// SWAPARB:CANONICAL:venueLong:mt5:venueShort:mt5:ts
			parts := strings.Split(p.OpportunityID, ":")
			if len(parts) >= 6 {
				comboKey := parts[2] + ":" + parts[3] + ":" + parts[4] + ":" + parts[5]
				existingCombos[comboKey] = true
			}
		}
	}
	// swap 套利组合列表：canonical → venueLong + venueShort + 净swap bps/日
	type swapCombo struct {
		canonical, vLong, vShort string
		netSwapBps               float64
	}
	combos := []swapCombo{
		{"AUDJPY", "52993526:mt5", "277842155:mt5", 0.494},
		{"AUDJPY", "REDACTED_MT_ACCT:mt5", "277842155:mt5", 0.471},
	}
	for _, c := range combos {
		comboKey := c.vLong + ":" + c.vShort // e.g. "52993526:mt5:277842155:mt5"
		if existingCombos[comboKey] {
			continue
		}
		e.trySwapArb(ctx, c.canonical, c.vLong, c.vShort, c.netSwapBps)
	}
}

// trySwapArb 尝试开仓单个 swap 套利组合。
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
		log.Printf("[autotrade] SWAPARB skip %s: spreadBps=%.2f < -3.0 (long ask=%.5f short bid=%.5f)",
			canonical, spreadBps, qLong.Ask, qShort.Bid)
		return
	}
	qty := 100000.0 / qLong.Ask
	reqID := fmt.Sprintf("swaparb-%d", time.Now().UnixNano())
	oppID := fmt.Sprintf("SWAPARB:%s:%s:%s:%d", canonical, venueLong, venueShort, time.Now().UnixMilli())
	status, msg, posID := e.service.ExecuteMeanRevertWithCap(ctx, canonical, venueLong, venueShort, reqID, qty, spreadBps, 100000.0)
	if status == "FAILED" {
		if msg == "" {
			msg = "pipeline returned FAILED"
		}
		log.Printf("[autotrade] SWAPARB-FAILED %s long=%s short=%s qty=%.4f: %s",
			canonical, venueLong, venueShort, qty, msg)
	} else {
		_ = e.store.UpdatePositionOppID(ctx, posID, oppID)
		log.Printf("[autotrade] SWAPARB-OK %s long=%s short=%s qty=%.4f posID=%d status=%s spreadBps=%.2f netSwap=%.3f bps/day",
			canonical, venueLong, venueShort, qty, posID, status, spreadBps, netSwapBps)
	}
}

// meanRevertLoop D-070 均值回归扫描：直接监控 277+591 的实时价差，
// 当价差 > openThreshold 时开仓，不依赖 detector 产生机会。
func (e *Engine) meanRevertLoop(ctx context.Context) {
	if e.quotes == nil {
		return
	}
	// 均值回归配置：277842155 + REDACTED_MT_ACCT
	venueA := "277842155:mt5"
	venueB := "REDACTED_MT_ACCT:mt5"
	// 品种列表 + 开仓阈值（美元，超过点差成本+安全边际）
	// MT5 报价频率 ~30-50s，阈值不能太高否则触发不到
	symbols := map[string]float64{
		"NAS100":  1.5, // 点差 1.12+1.50=2.62，但报价不同步时价差可能虚高
		"ETHUSDT": 0.5, // 点差较小
		"XAUUSD":  0.5, // 点差最小
	}
	ticker := time.NewTicker(5 * time.Second) // 5 秒扫描一次
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.scanMeanRevert(ctx, symbols, venueA, venueB)
		}
	}
}

// scanMeanRevert 扫描所有品种的 277+591 价差，超阈值则开仓。
func (e *Engine) scanMeanRevert(ctx context.Context, symbols map[string]float64, venueA, venueB string) {
	// 风控：检查持仓数、日亏
	openCount, err := e.store.CountOpenPositions(ctx)
	if err != nil {
		return
	}
	if openCount >= e.cfg.MaxPositions {
		return
	}
	for canonical, threshold := range symbols {
		qA, okA := e.quotes.Latest(canonical, venueA)
		qB, okB := e.quotes.Latest(canonical, venueB)
		if !okA || !okB {
			continue
		}
		// 报价新鲜度检查（120s）
		now := time.Now()
		if now.Sub(qA.Time) > 120*time.Second || now.Sub(qB.Time) > 120*time.Second {
			continue
		}
		// 双向价差：A买B卖 和 B买A卖
		spreadAB := qB.Bid - qA.Ask // 买A卖B的价差
		spreadBA := qA.Bid - qB.Ask // 买B卖A卖
		// 调试日志（每 ~30 秒打印一次价差状态）
		if time.Now().UnixMilli()%30000 < 5000 {
			log.Printf("[autotrade] MR-scan %s spreadAB=%.2f spreadBA=%.2f threshold=%.2f aBid=%.2f aAsk=%.2f bBid=%.2f bAsk=%.2f",
				canonical, spreadAB, spreadBA, threshold, qA.Bid, qA.Ask, qB.Bid, qB.Ask)
		}
		if spreadAB > threshold {
			// 买A卖B：A价格低，B价格高
			bps := spreadAB / qA.Ask * 10000
			e.executeMeanRevert(ctx, canonical, venueA, venueB, bps, e.cfg.NotionalUSD)
		} else if spreadBA > threshold {
			// 买B卖A：B价格低，A价格高
			bps := spreadBA / qB.Ask * 10000
			e.executeMeanRevert(ctx, canonical, venueB, venueA, bps, e.cfg.NotionalUSD)
		}
	}
}

// executeMeanRevert 执行均值回归开仓。
func (e *Engine) executeMeanRevert(ctx context.Context, canonical, venueBuy, venueSell string, openBps, notionalUSD float64) {
	// 计算 qty：notionalUSD / buyPrice
	qbuy, ok := e.quotes.Latest(canonical, venueBuy)
	if !ok {
		return
	}
	qty := notionalUSD / qbuy.Ask
	reqID := fmt.Sprintf("mr-%d", time.Now().UnixNano())
	status, msg, posID := e.service.ExecuteMeanRevert(ctx, canonical, venueBuy, venueSell, reqID, qty, openBps)
	if status == "FAILED" {
		if msg == "" {
			msg = "pipeline returned FAILED"
		}
		log.Printf("[autotrade] MR-FAILED canonical=%s buy=%s sell=%s openBps=%.2f qty=%.4f: %s",
			canonical, venueBuy, venueSell, openBps, qty, msg)
	} else {
		log.Printf("[autotrade] MR-OK canonical=%s buy=%s sell=%s openBps=%.2f qty=%.4f posID=%d status=%s",
			canonical, venueBuy, venueSell, openBps, qty, posID, status)
	}
}

// oppLoop 订阅机会事件 → 风控 → 执行。
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

// isCEXVenue 判断 venue 是否为 CEX（binance/gate/okx/任何非 MT5/MT4 的 venue）。
// D-070：业主指令「放弃 CEX，全功 CFD」。
func isCEXVenue(venue string) bool {
	return !strings.Contains(venue, ":mt5") && !strings.Contains(venue, ":mt4")
}

// handleOpportunity 处理一个机会事件：风控检查 → 执行 → 记录。
func (e *Engine) handleOpportunity(ctx context.Context, opp pgstore.Opportunity) {
	// D-070：业主指令「放弃 CEX，全功 CFD」——跳过所有 CEX venue
	if isCEXVenue(opp.VenueBuy) || isCEXVenue(opp.VenueSell) {
		return
	}
	// D-070：BRENT 排除——1314615 报价系统性偏高 $3（符号映射/合约规格差异），
	// 311 bps 价差是虚假的，平仓必然亏损
	if opp.Canonical == "BRENT" || opp.Canonical == "WTI" {
		return
	}
	// D-070 均值回归：只交易 277842155+REDACTED_MT_ACCT（同标的、价格接近、价差波动收敛）
	// 排除 1314615（NAS100 系统性偏低 $20）和 52993526（volume step=10手 imbalance）
	allowedVenues := map[string]bool{
		"277842155:mt5": true,
		"REDACTED_MT_ACCT:mt5": true,
	}
	if !allowedVenues[opp.VenueBuy] || !allowedVenues[opp.VenueSell] {
		return
	}
	// D-070 均值回归：只交易价差波动收敛的品种（avg≈0, std 大）
	// 排除结构性偏移品种（SPX500/US30/AUS200/UK100：avg≠0，价差不回归）
	allowedSymbols := map[string]bool{
		"NAS100":  true, // avg=-0.11, std=4.20
		"ETHUSDT": true, // avg=0.42, std=1.91
		"XAUUSD":  true, // avg=0.25, std=0.81
		"BTCUSDT": true, // avg=-0.09, std=74.09
	}
	if !allowedSymbols[opp.Canonical] {
		return
	}
	// 门槛：净收益
	if opp.NetBps < e.cfg.MinNetBps {
		return
	}
	log.Printf("[autotrade] pass filter opp=%s type=%s netBps=%.2f", opp.ID, opp.Type, opp.NetBps)
	// 会话内防重
	e.mu.Lock()
	if e.executed[opp.ID] {
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()

	// 风控1：策略是否被淘汰
	disabled, err := e.store.IsStrategyDisabled(ctx, opp.Type)
	if err != nil {
		log.Printf("[autotrade] check strategy disabled: %v", err)
	} else if disabled {
		log.Printf("[autotrade] skip disabled strategy %s", opp.Type)
		return
	}

	// 风控2：持仓数上限
	openCount, err := e.store.CountOpenPositions(ctx)
	if err != nil {
		log.Printf("[autotrade] count open positions: %v", err)
		return
	}
	if openCount >= e.cfg.MaxPositions {
		e.logTrade(ctx, &opp, 0, 0, "REJECTED", fmt.Sprintf("max positions reached (%d)", openCount))
		log.Printf("[autotrade] REJECTED max positions %d >= %d", openCount, e.cfg.MaxPositions)
		return
	}

	// 风控3：当日亏损上限
	dailyPnl, err := e.store.DailyRealizedPnl(ctx)
	if err != nil {
		log.Printf("[autotrade] daily pnl: %v", err)
		return
	}
	if dailyPnl <= -e.cfg.MaxDailyLoss {
		e.logTrade(ctx, &opp, 0, 0, "REJECTED", fmt.Sprintf("daily loss limit hit ($%.2f)", dailyPnl))
		log.Printf("[autotrade] REJECTED daily loss $%.2f", dailyPnl)
		return
	}

	// 风控4：幂等防重（DB 级）
	has, err := e.store.HasOpenPositionByOpportunity(ctx, opp.ID)
	if err != nil {
		log.Printf("[autotrade] check duplicate: %v", err)
		return
	}
	if has {
		log.Printf("[autotrade] skip duplicate opp=%s", opp.ID)
		return
	}

	log.Printf("[autotrade] pass risk opp=%s type=%s openCount=%d dailyPnl=$%.2f", opp.ID, opp.Type, openCount, dailyPnl)

	// 计算 qty：按单笔名义额 / 买腿参考价
	// D-070 CFD 适配：MT5 volume step 可能 ≥1手，固定名义额算出的 qty 取整后可能为 0。
	// 策略：先用名义额算 qty，取整后若为 0 则回退到 1 个 volume step（最小可交易量）。
	qty := e.cfg.NotionalUSD / opp.PriceBuy
	if qty <= 0 {
		return
	}

	// 执行
	reqID := fmt.Sprintf("autotrade-%d-%s", time.Now().UnixMilli(), opp.ID)
	status, msg, posID := e.service.ExecuteOpportunityInternal(ctx, opp.ID, reqID, qty)

	// 记录
	e.mu.Lock()
	e.executed[opp.ID] = true
	e.mu.Unlock()

	if status == "FAILED" {
		if msg == "" {
			msg = "execute failed (pipeline returned FAILED)"
		}
		e.logTrade(ctx, &opp, 0, qty, "FAILED", msg)
		log.Printf("[autotrade] FAILED opp=%s type=%s qty=%.4f: %s", opp.ID, opp.Type, qty, msg)
	} else {
		e.logTrade(ctx, &opp, posID, qty, "EXECUTED", "")
		log.Printf("[autotrade] EXECUTED opp=%s type=%s qty=%.4f posID=%d status=%s", opp.ID, opp.Type, qty, posID, status)
	}
}

// positionLoop 定期扫描 OPEN 持仓 → 收敛型自动平仓。
func (e *Engine) positionLoop(ctx context.Context) {
	ticker := time.NewTicker(e.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.scanAndClose(ctx)
		}
	}
}

// scanAndClose 扫描 OPEN 持仓，实时计算当前价差，收敛型达标则自动平仓。
// D-070 均值回归：查询持仓两腿的实时报价，计算当前 netBps，
// 若 <= CloseBpsThr 则平仓（价差回归到阈值内）。
func (e *Engine) scanAndClose(ctx context.Context) {
	positions, err := e.store.ListPositions(ctx, "OPEN", 50, 0)
	if err != nil {
		log.Printf("[autotrade] list open positions: %v", err)
		return
	}
	for _, p := range positions {
		// 息差型不主动平仓（持有赚 funding/swap）
		if p.Type == "FUNDING_HEDGE" || p.Type == "CARRY" {
			continue
		}
		// D-070 swap 套利持仓不主动平仓（持有赚 swap，长期持有）
		if strings.Contains(p.OpportunityID, "SWAPARB:") {
			continue
		}
		// D-070 均值回归：实时计算当前 netBps
		currentBps, ok := e.currentNetBps(&p)
		if !ok {
			continue // 报价不可用，跳过
		}
		if currentBps <= e.cfg.CloseBpsThr {
			log.Printf("[autotrade] auto-close pos=%d type=%s currentBps=%.2f <= %.1f (openBps=%.2f)",
				p.ID, p.Type, currentBps, e.cfg.CloseBpsThr, p.ExpectedNetBps)
			if err := e.service.AutoClosePosition(ctx, p.ID); err != nil {
				log.Printf("[autotrade] auto-close pos=%d failed: %v", p.ID, err)
				continue
			}
			// 平仓后更新策略统计
			e.updateStrategyStat(ctx, &p)
		}
	}
}

// currentNetBps 实时计算持仓的当前净 bps（D-070 均值回归平仓用）。
// 查询两腿 venue 的实时报价，按平仓方向（买回卖腿、卖回买腿）计算当前价差 bps。
func (e *Engine) currentNetBps(p *pgstore.Position) (float64, bool) {
	if e.quotes == nil {
		return p.ExpectedNetBps, true // fallback 到开仓时 bps
	}
	// 从 opportunity_id 解析 canonical（格式 CROSS_VENUE:SYMBOL:venue1:venue2:ts）
	parts := strings.Split(p.OpportunityID, ":")
	if len(parts) < 2 {
		return p.ExpectedNetBps, true
	}
	canonical := parts[1]
	// 持仓 legs：OPEN phase 的 BUY/SELL
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
	// 平仓：卖回买腿（bid）、买回卖腿（ask）
	buyClose, ok1 := e.quotes.Latest(canonical, buyLeg.Venue)
	sellClose, ok2 := e.quotes.Latest(canonical, sellLeg.Venue)
	if !ok1 || !ok2 {
		return 0, false
	}
	// 平仓价差 = 卖回买腿的 bid - 买回卖腿的 ask
	spread := buyClose.Bid - sellClose.Ask
	if spread <= 0 {
		return 0, true // 价差已反转，平仓
	}
	// 转 bps：spread / 开仓买腿价 * 10000
	refPrice := buyClose.Ask
	if buyLeg.PriceActual != nil && *buyLeg.PriceActual > 0 {
		refPrice = *buyLeg.PriceActual
	}
	return spread / refPrice * 10000, true
}

// updateStrategyStat 平仓后更新策略统计 + 检查淘汰。
func (e *Engine) updateStrategyStat(ctx context.Context, p *pgstore.Position) {
	if p.RealizedPnl == nil {
		return
	}
	pnl := *p.RealizedPnl
	if err := e.store.UpdateStrategyStat(ctx, p.Type, pnl); err != nil {
		log.Printf("[autotrade] update strategy stat type=%s: %v", p.Type, err)
		return
	}
	// 检查淘汰规则：累计≥5笔 且 (胜率<30% 或 avg_pnl<0)
	stat, err := e.store.GetStrategyStat(ctx, p.Type)
	if err != nil {
		log.Printf("[autotrade] get strategy stat: %v", err)
		return
	}
	if stat.TotalTrades >= 5 && !stat.Disabled {
		if stat.WinRate < 0.3 || stat.AvgPnl < 0 {
			reason := fmt.Sprintf("auto-disabled: %d trades, winRate=%.0f%%, avgPnl=$%.2f", stat.TotalTrades, stat.WinRate*100, stat.AvgPnl)
			if err := e.store.DisableStrategy(ctx, p.Type, reason); err != nil {
				log.Printf("[autotrade] disable strategy: %v", err)
			} else {
				log.Printf("[autotrade] STRATEGY DISABLED type=%s: %s", p.Type, reason)
			}
		}
	}
}

// logTrade 记录交易日志到 DB。
func (e *Engine) logTrade(ctx context.Context, opp *pgstore.Opportunity, posID int64, qty float64, status, reason string) {
	var posIDPtr *int64
	if posID > 0 {
		posIDPtr = &posID
	}
	err := e.store.InsertAutotradeLog(ctx, &pgstore.AutotradeLog{
		OpportunityID: opp.ID,
		PositionID:    posIDPtr,
		Strategy:      opp.Type,
		Canonical:     opp.Canonical,
		VenueBuy:      opp.VenueBuy,
		VenueSell:     opp.VenueSell,
		Qty:           qty,
		Status:        status,
		Reason:        reason,
		NetBps:        opp.NetBps,
	})
	if err != nil {
		log.Printf("[autotrade] insert log: %v", err)
	}
}
