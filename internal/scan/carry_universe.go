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
	LatestFactsByKind(ctx context.Context, kind string) ([]fact.Fact, error) // D-508 S3：kind-only 无 OR 专用查询
	// FactsWithin D-034：窗口样本读口（funding 7d 滚动均值——fact.FundingMeanAnnual 单实现）。
	FactsWithin(ctx context.Context, kind, venue, symbol string, since time.Time) ([]fact.Fact, error)
	// FactsWindowStats D-412 F3：窗口均值读口（swap 慢变量抗 flap；无样本=ErrNoFactsInWindow）。
	FactsWindowStats(ctx context.Context, kind, venue, symbol string, window time.Duration) (float64, error)
	ListSymbolMappings(ctx context.Context, venue string) ([]pgstore.SymbolMapping, error)
	ReplaceCarryUniverse(ctx context.Context, rows []pgstore.CarryUniverseRow) error
	// ListBrokerAccountStatuses D-388 S1.3：发现层 venue status 过滤（同 autotrade 收口谓词）。
	ListBrokerAccountStatuses(ctx context.Context) (map[string]string, error)
	// ListCexUniverse D-350：CEX 宇宙雷达表（spot_carry 第三结构数据源；first_seen 持续性闸锚）。
	ListCexUniverse(ctx context.Context) (map[string]pgstore.CexUniverseRow, error)
	// ListSymbolAliases D-464 S1：alias→canonical 语义别名表（Run 开头载一次）。
	ListSymbolAliases(ctx context.Context) (map[string]string, error)
	// ListSwapFreeAccounts D-028 ④：venue→swap_free 旗（true→universe join 该 MT 腿 swap=0，
	// 不等 7d 均值收敛；3001157 实证：spec swap 对 swap-free 账户为错值）。
	ListSwapFreeAccounts(ctx context.Context) (map[string]bool, error)
}

// UniverseRow 单 canonical×最优 venue 组合的净 carry 排名行。
type UniverseRow struct {
	Canonical    string
	MtVenue      string
	CexVenue     string
	NetAnnual    float64
	FundingAnn   float64 // D-034：决策+落表口径=facts funding 7d 滚动均值年化
	FundingInst  float64 // 瞬时最新值——仅展示列（entry/last_funding_ann；判定不消费）
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

// loadMtSwapInputs D-465：MT 侧输入装载共享（Run 与 StepPaperMtmCarry 同口径）——
// swap facts + venue status 过滤 + tmKnown + 别名表（D-464：双侧同一别名口径）；
// cex funding 侧由 Run 自载（mt×mt 不用）。
func (sc *CarryUniverseScanner) loadMtSwapInputs(ctx context.Context) (longF, shortF []fact.Fact,
	tmKnown func(fact.Fact) (int64, bool), aliases map[string]string, err error) {
	longF, err = sc.store.LatestFactsByKind(ctx, "swap_long")
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("swap_long facts: %w", err)
	}
	shortF, err = sc.store.LatestFactsByKind(ctx, "swap_short")
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("swap_short facts: %w", err)
	}
	// D-388 S1.3：broker 托管 venue 非 connected → facts 退出候选（停用即剔除，
	// 不等 facts 过期；同 autotrade discoverSwapCombos 收口谓词，fail-open 同惯例）。
	brokerStatuses := sc.activeVenueStatuses(ctx)
	longF = keepDiscoverableFacts(longF, brokerStatuses)
	shortF = keepDiscoverableFacts(shortF, brokerStatuses)
	// D-382 S1：trade_mode 消费——可执行性过滤数据源（facts 已在库零新数据源）。
	// 读取失败 fail-open 视同全未知（发现层不因数据缺席误杀，F5 同惯例）。
	tmFacts, err := sc.store.LatestFactsByKind(ctx, fact.KindTradeMode)
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
	tmKnown = func(f fact.Fact) (int64, bool) {
		tf, ok := tmByKey[f.Venue+":"+f.Symbol]
		if !ok {
			return 0, false
		}
		if sc.tradeModeTTL > 0 && time.Since(tf.Ts) > sc.tradeModeTTL {
			return 0, false
		}
		return int64(tf.Value), true
	}
	// D-464 S1：语义别名表载入（一轮一次）；载入失败=报错不静默（同 ListCexUniverse 路径）。
	rawAliases, err := sc.store.ListSymbolAliases(ctx)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("symbol aliases: %w", err)
	}
	aliases, dropped := cleanAliases(rawAliases)
	if len(dropped) > 0 {
		log.Printf("[scan] carry universe: %d alias 循环剔除: %v", len(dropped), dropped)
	}
	return longF, shortF, tmKnown, aliases, nil
}

// Run 一轮扫描：facts 近 24h 双面→join→结构计算→排序。
func (sc *CarryUniverseScanner) Run(ctx context.Context, since time.Time) (ScanResult, error) {
	res := ScanResult{}
	mtSwapLong, mtSwapShort, tmKnown, aliases, err := sc.loadMtSwapInputs(ctx)
	if err != nil {
		return res, err
	}
	cexFunding, err := sc.store.LatestFactsByKind(ctx, "funding")
	if err != nil {
		return res, fmt.Errorf("funding facts: %w", err)
	}
	brokerStatuses := sc.activeVenueStatuses(ctx)
	cexFunding = keepDiscoverableFacts(cexFunding, brokerStatuses)
	filteredCanonicals := map[string]bool{} // D-382 S2：有 swap fact 进入过滤评估的 canonical 集
	// MT swap facts D-412 F1/F2/F3：单位归一+venue 全保留+7d 窗口均值——收集逻辑
	// 提取至 carry_universe_collect.go（D-451 S3 纯搬家，注释全文随块搬移见彼）。
	longByCanon, shortByCanon := sc.collectMtSwaps(ctx, mtSwapLong, mtSwapShort, tmKnown, filteredCanonicals, &res, aliases, sc.swapFreeAccounts(ctx)) // D-028 ④
	// CEX funding：symbol→canonical（去 USDT）；F2 同款 venue 全保留（每 venue 一行，
	// 不再 abs 取最优——cex argmax 会使授权组合在 funding 排名易主日假 warn+保底失明）。
	// funding 符号定结构方向：>0=多 perp 付息→配空 CFD 收息；<0=反向。
	// D-034：val=decisions 口径 facts 7d 滚动均值（fact.FundingMeanAnnual 共享单实现，
	// 同 autotrade.fundingRateMeanForVenue）；inst=瞬时最新值仅留展示列。
	// 窗内零样本/查询败 → stale=读口不可证新鲜（fail-closed 剔除排名，同 F4 语义）。
	type cexF struct {
		venue, canonical string
		val              float64 // 7d 均值年化（net/suspect/open/adverse/zero_accrual 全走此值）
		inst             float64 // 瞬时最新值（展示列）
		stale            bool    // F4：均值不可证/最新样本龄期超闸（>UniverseFundingMaxAge）
	}
	fundByCanon := map[string]cexF{}
	for _, f := range cexFunding {
		canonical := applyAlias(NormalizeJoinKey(cexCanonical(f.Symbol)), aliases) // D-336R1+D-464：归一+别名
		if canonical == "" {
			res.Skipped++
			continue
		}
		mean, mts, mok := fact.FundingMeanAnnual(ctx, sc.store, f.Venue, f.Symbol, fact.FundingMeanWindow)
		fundByCanon[canonical+"|"+f.Venue] = cexF{
			venue: f.Venue, canonical: canonical, val: mean, inst: f.Value,
			stale: !mok || (sc.fundingMaxAge > 0 && time.Since(mts) > sc.fundingMaxAge),
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
					NetAnnual: 0, FundingAnn: cf.val, FundingInst: cf.inst, SwapFree: false,
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
					NetAnnual: cf.val + ml.val, FundingAnn: cf.val, FundingInst: cf.inst,
					SwapLongPct: &ml.val,
					SwapFree:    abs(ml.val) < swapFreeMaxPct, Structure: "long_cfd_short_perp",
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
					NetAnnual: ms.val - cf.val, FundingAnn: cf.val, FundingInst: cf.inst,
					SwapShortPct: &ms.val,
					SwapFree:     abs(ms.val) < swapFreeMaxPct, Structure: "short_cfd_long_perp",
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
			// D-028 ③：long/short 各发一行（原只发赢家——周末 funding=0 赢家翻转 short，
			// long 行被按 short 的负 net 判 adverse 退出 #64877 → 再 24h 冷却 → 攒不满 7 天）。
			sort.Slice(triple, func(i, j int) bool { return triple[i].NetAnnual > triple[j].NetAnnual })
			res.Rows = append(res.Rows, triple...)
		}
	}
	// D-350 阶段①：第三结构 spot_carry 段（D-382 S3 拆出 carry_universe_spot.go，纯搬移）。
	sc.appendSpotCarryRows(ctx, &res)
	sort.Slice(res.Rows, func(i, j int) bool { return res.Rows[i].NetAnnual > res.Rows[j].NetAnnual })
	return res, nil
}
