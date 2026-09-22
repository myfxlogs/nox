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
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// Store 扫描依赖（pgstore.Store 实现；LatestFacts 返回 fact.Fact 含 venue/symbol/value）。
type Store interface {
	LatestFacts(ctx context.Context, kind, venue, symbol string) ([]fact.Fact, error)
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

// CarryUniverseScanner 4h 扫描器。
type CarryUniverseScanner struct {
	store        Store
	tradeModeTTL time.Duration // D-382 S1：trade_mode fact 新鲜度（超期视同未知放行）
}

func NewCarryUniverseScanner(store Store) *CarryUniverseScanner {
	return &CarryUniverseScanner{store: store, tradeModeTTL: 24 * time.Hour}
}

// SetTradeModeTTL D-382 S1：注入 trade_mode 新鲜度（仿 FactCache.SetTradeModeTTL 惯例，
// wire_scan 装配传 cfg.TradeModeTTL）。≤0 = 不查新鲜度（全按存在性判定）。
func (sc *CarryUniverseScanner) SetTradeModeTTL(d time.Duration) {
	sc.tradeModeTTL = d
}

// modeAllowed D-382 S1：方向感知可交易性（detector.legTradable 语义对齐、独立实现防包耦合）。
// 买腿（long_cfd）要求 mode∈{LongOnly,Full}；卖腿（short_cfd）∈{ShortOnly,Full}；
// CloseOnly/Disabled 皆拦开新仓——用「允许集」判定不是「≠0」。
func modeAllowed(mode int64, buy bool) bool {
	if buy {
		return mode == fact.TradeModeLongOnly || mode == fact.TradeModeFull
	}
	return mode == fact.TradeModeShortOnly || mode == fact.TradeModeFull
}

// cexCanonical CEX symbol→canonical（detector/采集器现行规则同构：去 USDT 后缀）。
// BTCUSDT→BTC；无法映射（无 USDT 后缀）→ ""。
func cexCanonical(symbol string) string {
	if strings.HasSuffix(symbol, "USDT") && len(symbol) > 4 {
		return strings.TrimSuffix(symbol, "USDT")
	}
	return ""
}

// normalizeJoinKey D-336R1：join 键双侧规范化——canonical 双形分裂（MT map 侧
// XAUUSD vs CEX 侧 XAU）永不相等致黄金/BTC/PAXG 全 join 失败的层内根修
// （系统级两形并存=既有债不动）。去尾部 _、依次尝试去 USDT/USD 后缀
// （去后非空才去，USDT 优先）：XAUUSD→XAU、XAUUSDT→XAU、SPX500_→SPX500、BTCUSDT→BTC。
func normalizeJoinKey(s string) string {
	s = strings.TrimSuffix(s, "_")
	if r := strings.TrimSuffix(s, "USDT"); r != "" && r != s {
		return r
	}
	if r := strings.TrimSuffix(s, "USD"); r != "" && r != s {
		return r
	}
	return s
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
	// MT swap facts 按 symbol→canonical（broker_symbol_map join；同一 symbol 多 venue 取
	// swap 条件最好的组合）。
	type mtSwap struct {
		venue, canonical string
		val              float64
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
				canonical = normalizeJoinKey(m.Canonical) // D-336R1：双侧规范化后入键
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
		// 同 canonical 多 venue：取成本最低（值最大，负值里最接近 0）
		if cur, ok := longByCanon[canonical]; !ok || f.Value > cur.val {
			longByCanon[canonical] = mtSwap{venue: f.Venue, canonical: canonical, val: f.Value}
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
				canonical = normalizeJoinKey(m.Canonical) // D-336R1：双侧规范化后入键
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
		if cur, ok := shortByCanon[canonical]; !ok || f.Value > cur.val {
			shortByCanon[canonical] = mtSwap{venue: f.Venue, canonical: canonical, val: f.Value}
		}
	}
	// CEX funding：symbol→canonical（去 USDT）；同 canonical 多 venue 取 funding 绝对值最大
	//（funding 符号定结构方向：>0=多 perp 付息→配空 CFD 收息；<0=反向）。
	type cexF struct {
		venue, canonical string
		val              float64
	}
	fundByCanon := map[string]cexF{}
	for _, f := range cexFunding {
		canonical := normalizeJoinKey(cexCanonical(f.Symbol)) // D-336R1：双侧规范化后入键
		if canonical == "" {
			res.Skipped++
			continue
		}
		if cur, ok := fundByCanon[canonical]; !ok || abs(f.Value) > abs(cur.val) {
			fundByCanon[canonical] = cexF{venue: f.Venue, canonical: canonical, val: f.Value}
		}
	}
	// 结构计算：每 canonical×最优组合（两种结构取 net 大者）。
	for canonical, cf := range fundByCanon {
		candidates := []UniverseRow{}
		if ml, ok := longByCanon[canonical]; ok {
			// 多 CFD+空 perp：net=funding_ann−swap_long_cost（funding>0 空头收；swap_long<0 成本）
			net := cf.val - ml.val
			row := UniverseRow{
				Canonical: canonical, MtVenue: ml.venue, CexVenue: cf.venue,
				NetAnnual: net, FundingAnn: cf.val, SwapLongPct: &ml.val,
				SwapFree: ml.val == 0, Structure: "long_cfd_short_perp",
			}
			if note := suspectNote(ml.val, cf.val); note != "" { // D-345：脏源行 net=0 剔除排名
				row.NetAnnual, row.SwapFree, row.Note = 0, false, note
			}
			candidates = append(candidates, row)
		}
		if ms, ok := shortByCanon[canonical]; ok {
			// 空 CFD+多 perp：net=swap_short_recv−funding_ann（funding<0 多头收）
			net := ms.val - cf.val
			row := UniverseRow{
				Canonical: canonical, MtVenue: ms.venue, CexVenue: cf.venue,
				NetAnnual: net, FundingAnn: cf.val, SwapShortPct: &ms.val,
				SwapFree: ms.val == 0, Structure: "short_cfd_long_perp",
			}
			if note := suspectNote(ms.val, cf.val); note != "" { // D-345：脏源行 net=0 剔除排名
				row.NetAnnual, row.SwapFree, row.Note = 0, false, note
			}
			candidates = append(candidates, row)
		}
		if len(candidates) == 0 {
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
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].NetAnnual > candidates[j].NetAnnual })
		res.Rows = append(res.Rows, candidates[0])
	}
	// D-350 阶段①：第三结构 spot_carry 段（D-382 S3 拆出 carry_universe_spot.go，纯搬移）。
	sc.appendSpotCarryRows(ctx, &res)
	sort.Slice(res.Rows, func(i, j int) bool { return res.Rows[i].NetAnnual > res.Rows[j].NetAnnual })
	return res, nil
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
