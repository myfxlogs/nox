// Package engine 串联采集→检测→评估→存储的事件驱动管线（§5/§6/D-026）。
package engine

import (
	"context"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/detector"
	"arb/internal/evaluator"
	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
)

// AssetEquivView 引擎侧标的等价类消费口（D-300 裁 5：与 detector 共享同一
// pgstore.AssetEquivProvider 实例）。Partners 反查快照扩展；MaybeRefresh TTL 节流重载。
type AssetEquivView interface {
	Partners(canonical string) []string
	MaybeRefresh(ctx context.Context)
}

// opportunityStore 引擎所需的机会存储接口（D-050 测试注入：生产为 pgstore.Store，测试用 fake 判定 inserted）。
// 注：生产侧自 D-080 实施修正①起不再消费 inserted（oppTopic 发布条件去 inserted，见下方 upsert 后逻辑），
// bool 返回值仅保留以维持测试语义（fake 仍按 id 去重判定 inserted）。
type opportunityStore interface {
	UpsertOpportunity(ctx context.Context, o *pgstore.Opportunity) (bool, error)
}

// Engine 事件驱动管线：QuoteBus 报价事件 → 检测 → 评估 → 存储。
// commissionFor 按 venueKey（"{venue}:{venue_type}"）解析单腿佣金（bps/腿），D-036 per-venue 真实定价，
// 由 main 注入：CFD → broker_accounts.commission_bps（NULL 回退 config 默认），CEX → 逐所 taker 回退默认。
type Engine struct {
	bus *bus.QuoteBus
	// QuotesBus D-042 R1 ②：QuoteBus 只读访问（paper 双 CEX 轨报价连续性闸 LastChangeAt 用）。
	QuotesBus     *bus.QuoteBus
	detectors     []detector.Detector
	evalCfg       evaluator.Config
	store         opportunityStore
	facts         *FactCache
	commissionFor func(venueKey string) float64
	// pubDeltaBps 发布实质变化阈值（bps，ARB_OPP_PUBLISH_DELTA_BPS）；
	// pubHeartbeat TTL 续命心跳（=OpportunityTTL/3，语义=过期续命非判定节拍）。
	pubDeltaBps  float64
	pubHeartbeat time.Duration
	// oppTopic 新机会站内推送总线（D-050；修复⑭ S3 退役发布门槛 → 全量事件流）：
	// upsert 成功即发布（oppTopic 定性为全量事件流，UI 侧 30s 快照+前端默认 ≥5 显示过滤兜底）。
	// nil = 未接线（测试可不接）。
	oppTopic *bus.Topic[pgstore.Opportunity]
	// minStoreNetBps CROSS_VENUE 入库最低净收益（bps）；D-078 round-trip 修正：
	// 低于此值的价差型机会不入库（从源头消除垃圾）。仅适用于 CROSS_VENUE（net 为一次性 bps）；
	// 息差型 net 为年化百分点，由下方三净门槛过滤（D-085）。
	minStoreNetBps float64
	// 息差家族入库净门槛（D-085：门槛作用在 evaluator 扣成本后的 NetBps=年化百分点）。
	// 检测器门槛保留为前置过滤（双保险无害）；零值=不过滤（测试默认）。
	fundingHedgeMinNetStore float64
	perpFundingMinNetStore  float64
	carryMinNetStore        float64
	xassetCarryMinNetStore  float64
	// assetEquiv 跨标的等价类映射（D-300 G1 快照扩展）。nil=无等价类（存量 4 型行为不变）。
	assetEquiv AssetEquivView
	// excludeCanonicals 检测产出排除集合（D-072 资金提醒弹窗提示词 B1：复用 ARB_AUTOTRADE_EXCLUDE_CANONICALS
	// 双用途——执行层排除 + 检测层排除，P3 单一真相）。nil/空 = 不排除（测试默认）。
	// 命中时机会不入库（TTL 自然过期不适用——根本不产），从源头消除 BRENT/WTI 假机会（1314615 报价偏高 $3，D-068 实证）。
	excludeCanonicals map[string]bool
	// D-555 S1：双CEX 纸面轨候选钩子（FUNDING_HEDGE 候选直传；nil=未接线零开销）。
	cexPaperHook func(context.Context, []detector.Candidate)
	// disabledStrategies 自动淘汰策略集合（§1 自动淘汰闭环）：strategy_stats.disabled=true 的策略名。
	// disabled 策略产出的机会不入库——系统已自证亏损，展示假机会误导。定期刷新（main goroutine），
	// 与 onQuote 并发 → disabledMu 保护。nil/空 = 不排除（测试默认）。
	disabledStrategies map[string]bool
	disabledMu         sync.RWMutex

	mu          sync.Mutex
	lastPub     map[string]pubState  // oppID → 上次发布态（engine_publish.go；发布/落库去重）
	paperHookAt map[string]time.Time // canonical → 上次 cexPaperHook 调用（30s 采样节拍）
}

// New 创建引擎。commissionFor 必填（成本正确性依赖真实佣金，D-036），nil = 装配错误直接 panic。
// oppTopic 允许 nil（测试不接推送）；修复⑭ S3 退役 minNotifyNetBps 发布门槛 → oppTopic 全量事件流。
// minStoreNetBps 为 CROSS_VENUE 入库门槛（D-078，缺省 0 = 不过滤）。
// fundingHedgeMinNetStore/perpFundingMinNetStore/carryMinNetStore 为息差家族入库净门槛
// （D-085，作用在 evaluator 扣成本后的 NetBps=年化百分点，缺省 0 = 不过滤）。
func New(b *bus.QuoteBus, detectors []detector.Detector, cfg evaluator.Config, store opportunityStore, fc *FactCache, commissionFor func(venueKey string) float64, pubDeltaBps float64, oppTopic *bus.Topic[pgstore.Opportunity], minStoreNetBps, fundingHedgeMinNetStore, perpFundingMinNetStore, carryMinNetStore, xassetCarryMinNetStore float64) *Engine {
	if commissionFor == nil {
		panic("engine: commissionFor resolver required (D-036 per-venue 佣金)")
	}
	if pubDeltaBps <= 0 {
		pubDeltaBps = 0.5
	}
	pubHeartbeat := cfg.OpportunityTTL / 3
	if pubHeartbeat <= 0 {
		pubHeartbeat = 10 * time.Second
	}
	return &Engine{
		bus:                     b,
		QuotesBus:               b,
		detectors:               detectors,
		evalCfg:                 cfg,
		store:                   store,
		facts:                   fc,
		commissionFor:           commissionFor,
		pubDeltaBps:             pubDeltaBps,
		pubHeartbeat:            pubHeartbeat,
		oppTopic:                oppTopic,
		minStoreNetBps:          minStoreNetBps,
		fundingHedgeMinNetStore: fundingHedgeMinNetStore,
		perpFundingMinNetStore:  perpFundingMinNetStore,
		carryMinNetStore:        carryMinNetStore,
		xassetCarryMinNetStore:  xassetCarryMinNetStore,
		lastPub:                 make(map[string]pubState),
		paperHookAt:             make(map[string]time.Time),
	}
}

// Run 订阅 QuoteBus 并驱动检测→评估→存储循环。阻塞至 ctx 取消。
func (e *Engine) Run(ctx context.Context) {
	ch, cancel := e.bus.Subscribe("")
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case q, ok := <-ch:
			if !ok {
				return
			}
			e.onQuote(ctx, q)
		}
	}
}

// onQuote 处理单条报价事件（D-004 P1#8：每帧内存判定，无节流）：
// 新鲜度 → 快照 → 检测 → 评估 → 发布去重（maybePublish：实质变化 ≥pubDeltaBps 或
// TTL 心跳到期才 Upsert+Publish——发布/落库不再每帧，下游 autotrade oppLoop 与前端不被灌）。
func (e *Engine) onQuote(ctx context.Context, q bus.Quote) {
	// 顺手清理 lastPub 过期条目（at 早于 2×OpportunityTTL——地图只增会泄漏）。
	e.prunePub()

	// 新鲜度检查（§5.0）
	if time.Since(q.Time) > e.evalCfg.DetectorFreshnessTTL {
		return
	}

	// D-300 G1 快照扩展：q.Symbol ∪ Partners(q.Symbol)——存量 canonical partners 为空时零影响。
	// MaybeRefresh 由报价事件驱动（TTL 内不重查）；detector 共享同一实例禁 Scan 内查库（裁 5）。
	symbols := []string{q.Symbol}
	if e.assetEquiv != nil {
		e.assetEquiv.MaybeRefresh(ctx)
		symbols = append(symbols, e.assetEquiv.Partners(q.Symbol)...)
	}
	snapshot := e.bus.Snapshot(ctx, symbols)
	candidates := detector.ScanAll(e.detectors, snapshot, e.facts)

	for _, c := range candidates {
		result := e.evaluate(ctx, c, snapshot)
		if result == nil {
			continue
		}
		if result.Rating == evaluator.RatingTrap {
			continue
		}
		o := resultToOpp(result)
		// D-072 资金提醒弹窗提示词 B1：检测层排除 BRENT/WTI 等假机会（复用 ARB_AUTOTRADE_EXCLUDE_CANONICALS，
		// 执行层+检测层双用途，P3 单一真相）。命中 → 不入库（从源头消除，TTL 自然过期不适用）。
		if len(e.excludeCanonicals) > 0 && e.excludeCanonicals[o.Canonical] {
			continue
		}
		// §1 自动淘汰闭环：strategy_stats.disabled=true 的策略产出不入库——系统已自证亏损，
		// 展示假机会误导。与 gate 层 disabled 检查呼应（gate 阻执行，此处阻产出+展示）。
		if e.isStrategyDisabled(o.Type) {
			continue
		}
		// D-078 round-trip 入库门槛：CROSS_VENUE net 为一次性 bps，低于门槛不入库（从源头消除垃圾）。
		// D-085 息差家族净门槛：门槛作用在 evaluator 扣成本后的 NetBps（年化百分点），
		// 面板亮灯=净口径真过线；检测器门槛保留为前置过滤（双保险无害）。零值=不过滤（测试默认）。
		if !netStoreGate(o, e) {
			continue
		}
		// D-080 实施修正① + 修复⑭ S3：oppTopic 双用途（D-050 站内推送 + autotrade 事件流）。
		// D-086 实施修正①：撤验证级别发布门——oppTopic 是双用途数据总线（autotrade rollup 喂入 +
		// 前端实时流），永不论证级别过滤；V0/V1 区分只存在于显示层（徽章，service_verification 填）。
		// D-004 P1#8：发布/落库走实质变化+TTL 心跳去重（maybePublish），UI 快照兜底不变。
		e.maybePublish(ctx, o)
	}
	// D-051 ①：CEX 纸面证据面消费 detector Allow 前候选流（执行链仍走上方
	// ScanAll 后闸门，零改动；合流/采样节拍见 paperCexUnfiltered）。
	e.paperCexUnfiltered(ctx, snapshot)
}

func (e *Engine) evaluate(ctx context.Context, c detector.Candidate, snapshot map[string]map[string]bus.Quote) *evaluator.Result {
	// 查买/卖腿报价获取 spread（D-300 G4：卖腿用 CanonicalSell，空回退 Canonical——同标的机会不变）
	sellCanonical := c.CanonicalSell
	if sellCanonical == "" {
		sellCanonical = c.Canonical
	}
	buyQ := findQuote(snapshot, c.Canonical, c.VenueBuy)
	sellQ := findQuote(snapshot, sellCanonical, c.VenueSell)
	if buyQ == nil || sellQ == nil {
		return nil
	}

	spreadBuy := (buyQ.Ask - buyQ.Bid) / 2
	spreadSell := (sellQ.Ask - sellQ.Bid) / 2

	// 佣金（bps/腿，D-036 per-venue 解析：CFD 账号真实值/CEX 逐所 taker，均回退 config 默认）
	commissionBps := e.commissionFor(c.VenueBuy) + e.commissionFor(c.VenueSell)

	// 息差型日息差由评估器从净年化推导，无需再取 swap Fact（D-036 简化死参数）
	return evaluator.Evaluate(c, e.evalCfg, spreadBuy, spreadSell, commissionBps, 0)
}

// netStoreGate 按 o.Type 分派入库净门槛（D-078 CROSS_VENUE bps / D-085 息差家族年化百分点）。
// 零值门槛=不过滤（测试默认放行）；false=不入库（同 trap 处理，从源头消除）。
// CROSS_VENUE net 为一次性 bps；息差型 net 为年化百分点，勿混用同一把尺子。
func netStoreGate(o *pgstore.Opportunity, e *Engine) bool {
	switch o.Type {
	case "CROSS_VENUE":
		return !(e.minStoreNetBps > 0 && o.NetBps < e.minStoreNetBps)
	case "FUNDING_HEDGE":
		return !(e.fundingHedgeMinNetStore > 0 && o.NetBps < e.fundingHedgeMinNetStore)
	case "PERP_FUNDING":
		return !(e.perpFundingMinNetStore > 0 && o.NetBps < e.perpFundingMinNetStore)
	case "CARRY":
		return !(e.carryMinNetStore > 0 && o.NetBps < e.carryMinNetStore)
	case "XASSET_CARRY":
		return !(e.xassetCarryMinNetStore > 0 && o.NetBps < e.xassetCarryMinNetStore)
	default:
		return true
	}
}

func findQuote(snapshot map[string]map[string]bus.Quote, canonical, venueKey string) *bus.Quote {
	venues, ok := snapshot[canonical]
	if !ok {
		return nil
	}
	// venueKey 格式 "{venue}:{venueType}"
	for _, q := range venues {
		if keydomain.Join(q.Venue, q.VenueType) == venueKey {
			return &q
		}
	}
	return nil
}

func resultToOpp(r *evaluator.Result) *pgstore.Opportunity {
	// D-300：跨标的配对列——空串转 NULL（非 XASSET_CARRY 不留空串）。
	var canonicalSell, assetClass *string
	if r.CanonicalSell != "" {
		canonicalSell = &r.CanonicalSell
	}
	if r.AssetClass != "" {
		assetClass = &r.AssetClass
	}
	return &pgstore.Opportunity{
		ID:             r.ID,
		Type:           r.Type,
		Canonical:      r.Canonical,
		CanonicalSell:  canonicalSell,
		AssetClass:     assetClass,
		VenueBuy:       r.VenueBuy,
		VenueSell:      r.VenueSell,
		PriceBuy:       r.PriceBuy,
		PriceSell:      r.PriceSell,
		GrossBps:       r.GrossBps,
		SpreadCost:     r.SpreadCost,
		CommissionCost: r.CommissionCost,
		SlippageCost:   r.SlippageCost,
		SwapCost:       r.SwapCost,
		NetBps:         r.NetBps,
		BreakevenDays:  r.BreakevenDays,
		QuoteTime:      r.QuoteTime,
		ExpiresAt:      r.ExpiresAt,
	}
}
