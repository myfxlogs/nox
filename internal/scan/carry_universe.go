// Package scan D-336：carry 宇宙扫描器（发现层根修——「自动发现」从口号变机制）。
// 数据双面在库（MT swap facts×CEX funding facts）零联合消费的根因闭环：
// 每 4h（结算节奏）join 一遍全宇宙算净 carry 排名→快照表+digest+阈值告警。
// 纯发现层：不产生 opportunity、不触发开仓（发现≠授权，D-324/158 纪律不动）。
package scan

import (
	"context"
	"fmt"
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
	store Store
}

func NewCarryUniverseScanner(store Store) *CarryUniverseScanner {
	return &CarryUniverseScanner{store: store}
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

// SUSPECT_PCT D-345：输入腿年化疑源线（%）。DOT 脏源实证（broker 1314615 DOTUSDT
// swap_long=−469.5%/yr 离谱定价致假机会霸榜=发现层失能）：越界行不删（保观测），
// net=0+swap_free=false+note 剔除排名，digest/告警自然排除。
const SUSPECT_PCT = 100.0

// suspectNote D-345：任一输入腿 |年化|>SUSPECT_PCT → 返回疑因 note；干净组合返回 ""。
func suspectNote(swapPct, fundingPct float64) string {
	parts := make([]string, 0, 2)
	if abs(swapPct) > SUSPECT_PCT {
		parts = append(parts, fmt.Sprintf("swap %.1f%%", abs(swapPct)))
	}
	if abs(fundingPct) > SUSPECT_PCT {
		parts = append(parts, fmt.Sprintf("funding %.1f%%", abs(fundingPct)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "suspect: " + strings.Join(parts, ", ")
}

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
			continue
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].NetAnnual > candidates[j].NetAnnual })
		res.Rows = append(res.Rows, candidates[0])
	}
	sort.Slice(res.Rows, func(i, j int) bool { return res.Rows[i].NetAnnual > res.Rows[j].NetAnnual })
	return res, nil
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
