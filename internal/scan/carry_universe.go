// Package scan D-336：carry 宇宙扫描器（发现层根修——「自动发现」从口号变机制）。
// 数据双面在库（MT swap facts×CEX funding facts）零联合消费的根因闭环：
// 每 4h（结算节奏）join 一遍全宇宙算净 carry 排名→快照表+digest+阈值告警。
// 纯发现层：不产生 opportunity、不触发开仓（发现≠授权，D-324/158 纪律不动）。
package scan

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// Store 扫描依赖（pgstore.Store 实现；LatestFacts 返回 fact.Fact 含 venue/symbol/value）。
type Store interface {
	LatestFacts(ctx context.Context, kind, venue, symbol string) ([]fact.Fact, error)
	// FactsWindowStats D-412 F3：窗口均值读口（swap 慢变量抗 flap；无样本=ErrNoFactsInWindow）。
	FactsWindowStats(ctx context.Context, kind, venue, symbol string, window time.Duration) (float64, error)
	ListSymbolMappings(ctx context.Context, venue string) ([]pgstore.SymbolMapping, error)
	ReplaceCarryUniverse(ctx context.Context, rows []pgstore.CarryUniverseRow) error
	// ListBrokerAccountStatuses D-388 S1.3：发现层 venue status 过滤（同 autotrade 收口谓词）。
	ListBrokerAccountStatuses(ctx context.Context) (map[string]string, error)
	// ListCexUniverse D-350：CEX 宇宙雷达表（spot_carry 第三结构数据源；first_seen 持续性闸锚）。
	ListCexUniverse(ctx context.Context) (map[string]pgstore.CexUniverseRow, error)
}

// UniverseRow 单 canonical×最优 venue 组合的净 carry 排名行。
type UniverseRow struct {
	Canonical    string
	MtVenue      string
	CexVenue     string
	NetAnnual    float64
	FundingAnn   float64
	SwapLongPct  *float64
	SwapShortPct *float64
	SwapFree     bool
	Structure    string
	Note         string // D-345：suspect 疑因标注（干净组合空串）
}

// ScanResult 一轮扫描输出（每 canonical 取 max(net) 一行，net 降序）。
type ScanResult struct {
	Rows    []UniverseRow
	Skipped int // join 失败（MT symbol 无映射/CEX symbol 无法 canonical 化）行计数
}

// D-412 F4：读口龄期闸常量（env 可选 ARB_UNIVERSE_FUNDING_MAX_AGE / ARB_UNIVERSE_SWAP_MAX_AGE
// 覆盖，wire 装配层解析——D-407 先例 scan 层不读 env）。
const (
	UniverseFundingMaxAge = 24 * time.Hour
	UniverseSwapMaxAge    = 48 * time.Hour
	// swapWindowTTL F3：swap 窗口均值回看期（慢变量抗 flap；39050818 0/−1.4133 交替实证）。
	swapWindowTTL  = 7 * 24 * time.Hour
	swapFreeMaxPct = 1.0 // swap_free 判定：|7d 均值年化| < 1%/yr（R2 裁定 magnitude 单指标）
)

// CarryUniverseScanner 4h 扫描器。
type CarryUniverseScanner struct {
	store         Store
	tradeModeTTL  time.Duration // D-382 S1：trade_mode fact 新鲜度（超期视同未知放行）
	fundingMaxAge time.Duration // D-412 F4：funding fact 龄期闸（超期行 net=0+note=stale）
	swapMaxAge    time.Duration // D-412 F4：swap fact 龄期闸
}

func NewCarryUniverseScanner(store Store) *CarryUniverseScanner {
	return &CarryUniverseScanner{
		store:         store,
		tradeModeTTL:  24 * time.Hour,
		fundingMaxAge: UniverseFundingMaxAge,
		swapMaxAge:    UniverseSwapMaxAge,
	}
}

// SetAgeGates D-412 F4：龄期闸注入（wire 装配传 env 覆盖值；≤0=该侧不设闸）。
func (sc *CarryUniverseScanner) SetAgeGates(funding, swap time.Duration) {
	sc.fundingMaxAge, sc.swapMaxAge = funding, swap
}

// SetTradeModeTTL D-382 S1：注入 trade_mode 新鲜度（仿 FactCache.SetTradeModeTTL 惯例，
// wire_scan 装配传 cfg.TradeModeTTL）。≤0 = 不查新鲜度（全按存在性判定）。
func (sc *CarryUniverseScanner) SetTradeModeTTL(d time.Duration) {
	sc.tradeModeTTL = d
}

// SPOT_CARRY_COST_PCT D-350：spot-carry 双边成本折算（%/yr）≈ binance 现货 taker 0.1% +
// perp taker 0.05% 单边摊一年持有期（粗折算，纸面轨/demo 用真实成交校准）。
const SPOT_CARRY_COST_PCT = 0.15

// spotCarryMinAge D-350：持续性闸——fin_cex_universe.first_seen 距今 ≥48h 才入候选
// （瞬态上市窗 spike 不入，D-328 概念复用；GUSDT 9-16 起=3 天实证）。
const spotCarryMinAge = 48 * time.Hour

// Run 一轮扫描：facts 近 24h 双面→join→结构计算→排序。
func (sc *CarryUniverseScanner) Run(ctx context.Context, since time.Time) (ScanResult, error) {
	res := ScanResult{}
	mtSwapLong, err := sc.store.LatestFacts(ctx, "swap_long", "", "")
	if err != nil {
		return res, fmt.Errorf("swap_long facts: %w", err)
	}
	mtSwapShort, err := sc.store.LatestFacts(ctx, "swap_short", "", "")
	if err != nil {
		return res, fmt.Errorf("swap_short facts: %w", err)
	}
	cexFunding, err := sc.store.LatestFacts(ctx, "funding", "", "")
	if err != nil {
		return res, fmt.Errorf("funding facts: %w", err)
	}
	// D-388 S1.3：broker 托管 venue 非 connected → facts 退出候选（停用即剔除，
	// 不等 facts 过期；同 autotrade discoverSwapCombos 收口谓词，fail-open 同惯例）。
	brokerStatuses := sc.activeVenueStatuses(ctx)
	mtSwapLong = keepDiscoverableFacts(mtSwapLong, brokerStatuses)
	mtSwapShort = keepDiscoverableFacts(mtSwapShort, brokerStatuses)
	cexFunding = keepDiscoverableFacts(cexFunding, brokerStatuses)
	// D-382 S1：trade_mode 消费——可执行性过滤数据源（facts 已在库零新数据源）。
	// 读取失败 fail-open 视同全未知（发现层不因数据缺席误杀，F5 同惯例）。
	tmFacts, err := sc.store.LatestFacts(ctx, fact.KindTradeMode, "", "")
	if err != nil {
		log.Printf("[scan] carry universe: trade_mode facts: %v（视同全未知放行）", err)
		tmFacts = nil
	}
	tmByKey := make(map[string]fact.Fact, len(tmFacts))
	for _, f := range tmFacts {
		tmByKey[f.Venue+":"+f.Symbol] = f
	}
	// tmKnown trade_mode 已知且新鲜判定：超 tradeModeTTL 视同未知（venue 停报时陈旧
	// mode=0 不得永久压死已解禁候选——「不逃脱」公理，A7 锚）。
	tmKnown := func(f fact.Fact) (int64, bool) {
		tf, ok := tmByKey[f.Venue+":"+f.Symbol]
		if !ok {
			return 0, false
		}
		if sc.tradeModeTTL > 0 && time.Since(tf.Ts) > sc.tradeModeTTL {
			return 0, false
		}
		return int64(tf.Value), true
	}
	filteredCanonicals := map[string]bool{} // D-382 S2：有 swap fact 进入过滤评估的 canonical 集
	// MT swap facts D-412 F1/F2/F3：单位归一（bps_daily×365/100→%/yr 单一换算点）+
	// venue 全保留（F2 全叉积：归并键=canonical|mt_venue，同 canonical 多 venue 不再坍缩
	// 取最优——授权在跑线必须恒在监控视野，D-410 保底槽三重键前提）+ 7d 窗口均值取值
	// （F3 抗 flap：swap_free=|均值年化|<1%；点值判定=抛硬币 39050818 实证）。
	type mtSwap struct {
		venue, canonical string
		val              float64 // 年化 %/yr（F1 换算后；F3 起为 7d 均值年化）
		ts               time.Time
		stale            bool // F4：fact 龄期超闸（>UniverseSwapMaxAge）
	}
	longByCanon := map[string]mtSwap{}
	shortByCanon := map[string]mtSwap{}
	for _, f := range mtSwapLong {
		if f.Symbol == "" || f.Value > 0 {
			continue // 正值=收息（多 CFD 不付息不可能）非候选；0=免息腿（swap_free 候选保留）
		}
		maps, err := sc.store.ListSymbolMappings(ctx, f.Venue)
		if err != nil {
			continue
		}
		canonical := ""
		for _, m := range maps {
			if m.BrokerSymbol == f.Symbol && m.Canonical != "" {
				canonical = NormalizeJoinKey(m.Canonical) // D-336R1：双侧规范化后入键
				break
			}
		}
		if canonical == "" {
			res.Skipped++
			continue
		}
		// D-382 S1：可执行性过滤——买腿要求 mode∈{LongOnly,Full}；已知且禁 → skip 该 fact
		// （fact 级过滤=最优 venue 被禁时归并自然落次优可交易 venue，canonical 存活不逃脱）。
		if mode, known := tmKnown(f); known && !modeAllowed(mode, true) {
			filteredCanonicals[canonical] = true
			continue
		}
		mean, err := sc.store.FactsWindowStats(ctx, "swap_long", f.Venue, f.Symbol, swapWindowTTL)
		if err != nil {
			continue // F3：窗口零样本/读失败=不产行（既有 fail-closed 语义不变）
		}
		val := mean * 365 / 100
		longByCanon[canonical+"|"+f.Venue] = mtSwap{
			venue: f.Venue, canonical: canonical, val: val, ts: f.Ts,
			stale: sc.swapMaxAge > 0 && time.Since(f.Ts) > sc.swapMaxAge,
		}
	}
	for _, f := range mtSwapShort {
		if f.Symbol == "" || f.Value < 0 {
			continue // 负值=付息非候选；0=免息腿（swap_free 候选保留）
		}
		maps, err := sc.store.ListSymbolMappings(ctx, f.Venue)
		if err != nil {
			continue
		}
		canonical := ""
		for _, m := range maps {
			if m.BrokerSymbol == f.Symbol && m.Canonical != "" {
				canonical = NormalizeJoinKey(m.Canonical) // D-336R1：双侧规范化后入键
				break
			}
		}
		if canonical == "" {
			res.Skipped++
			continue
		}
		// D-382 S1：卖腿要求 mode∈{ShortOnly,Full}（同 long 循环，fact 级过滤 venue 回退）。
		if mode, known := tmKnown(f); known && !modeAllowed(mode, false) {
			filteredCanonicals[canonical] = true
			continue
		}
		mean, err := sc.store.FactsWindowStats(ctx, "swap_short", f.Venue, f.Symbol, swapWindowTTL)
		if err != nil {
			continue // F3：窗口零样本/读失败=不产行（既有 fail-closed 语义不变）
		}
		val := mean * 365 / 100
		shortByCanon[canonical+"|"+f.Venue] = mtSwap{
			venue: f.Venue, canonical: canonical, val: val, ts: f.Ts,
			stale: sc.fundingMaxAge > 0 && time.Since(f.Ts) > sc.fundingMaxAge,
		}
	}
	// CEX funding：symbol→canonical（去 USDT）；F2 同款 venue 全保留（每 venue 一行，
	// 不再 abs 取最优——cex argmax 会使授权组合在 funding 排名易主日假 warn+保底失明）。
	// funding 符号定结构方向：>0=多 perp 付息→配空 CFD 收息；<0=反向。
	type cexF struct {
		venue, canonical string
		val              float64
		ts               time.Time
		stale            bool // F4：funding 龄期超闸（>UniverseFundingMaxAge）
	}
	fundByCanon := map[string]cexF{}
	for _, f := range cexFunding {
		canonical := NormalizeJoinKey(cexCanonical(f.Symbol)) // D-336R1：双侧规范化后入键
		if canonical == "" {
			res.Skipped++
			continue
		}
		fundByCanon[canonical+"|"+f.Venue] = cexF{
			venue: f.Venue, canonical: canonical, val: f.Value,
			stale: sc.fundingMaxAge > 0 && time.Since(f.Ts) > sc.fundingMaxAge,
		}
	}
	// 结构计算：D-412 F2 全叉积——每 (canonical, mt_venue, cex_venue) 三元组一行
	//（两种结构取 net 大者），任意授权三元组恒可命中（D-410 保底段三重键回归锚 ②）。
	for _, cf := range fundByCanon {
		canonical := cf.canonical
		mtVenues := map[string]bool{}
		for _, ml := range longByCanon {
			if ml.canonical == canonical {
				mtVenues[ml.venue] = true
			}
		}
		for _, ms := range shortByCanon {
			if ms.canonical == canonical {
				mtVenues[ms.venue] = true
			}
		}
		if len(mtVenues) == 0 {
			// D-382 S2：全禁标记行——三条件全满足才产（v2 钉死）：①canonical∈fundByCanon
			// （无 funding 本就不产行=正确静默）②有 swap fact 进入过滤评估 ③双向归并零存活。
			// 行语义=D-345 suspect 先例（脏行可见、net=0 沉底、告警天然不触）+D-350 (spot)
			// 哨兵 venue 先例；venue 日后解禁 → facts 更新 → 行自动复活为真候选。
			if filteredCanonicals[canonical] {
				res.Rows = append(res.Rows, UniverseRow{
					Canonical: canonical, MtVenue: "(all-disabled)", CexVenue: cf.venue,
					NetAnnual: 0, FundingAnn: cf.val, SwapFree: false,
					Note: "mt_trade_disabled",
				})
			}
			continue
		}
		for venue := range mtVenues {
			triple := []UniverseRow{}
			if ml, ok := longByCanon[canonical+"|"+venue]; ok {
				// 多 CFD+空 perp：net=funding_ann+swap_long（F1 年化后；swap_long≤0 成本负值
				// 直加——D1 方向性错误一并闭环，锚① INJ 算术=SSOT 写死 11−236.8=−225.8）
				row := UniverseRow{
					Canonical: canonical, MtVenue: ml.venue, CexVenue: cf.venue,
					NetAnnual: cf.val + ml.val, FundingAnn: cf.val, SwapLongPct: &ml.val,
					SwapFree: abs(ml.val) < swapFreeMaxPct, Structure: "long_cfd_short_perp",
				}
				if note := suspectNote(ml.val, cf.val); note != "" { // D-345：脏源行 net=0 剔除排名
					row.NetAnnual, row.SwapFree, row.Note = 0, false, note
				}
				if ml.stale || cf.stale { // F4：龄期超闸=读口不诚实，剔除排名（D-345 同族形态）
					row.NetAnnual, row.SwapFree, row.Note = 0, false, "stale"
				}
				triple = append(triple, row)
			}
			if ms, ok := shortByCanon[canonical+"|"+venue]; ok {
				// 空 CFD+多 perp：net=swap_short_recv−funding_ann（funding<0 多头收）
				row := UniverseRow{
					Canonical: canonical, MtVenue: ms.venue, CexVenue: cf.venue,
					NetAnnual: ms.val - cf.val, FundingAnn: cf.val, SwapShortPct: &ms.val,
					SwapFree: abs(ms.val) < swapFreeMaxPct, Structure: "short_cfd_long_perp",
				}
				if note := suspectNote(ms.val, cf.val); note != "" { // D-345：脏源行 net=0 剔除排名
					row.NetAnnual, row.SwapFree, row.Note = 0, false, note
				}
				if ms.stale || cf.stale { // F4
					row.NetAnnual, row.SwapFree, row.Note = 0, false, "stale"
				}
				triple = append(triple, row)
			}
			if len(triple) == 0 {
				continue
			}
			sort.Slice(triple, func(i, j int) bool { return triple[i].NetAnnual > triple[j].NetAnnual })
			res.Rows = append(res.Rows, triple[0])
		}
	}
	// D-350 阶段①：第三结构 spot_carry 段（D-382 S3 拆出 carry_universe_spot.go，纯搬移）。
	sc.appendSpotCarryRows(ctx, &res)
	sort.Slice(res.Rows, func(i, j int) bool { return res.Rows[i].NetAnnual > res.Rows[j].NetAnnual })
	return res, nil
}
