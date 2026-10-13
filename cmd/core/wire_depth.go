// wire_depth.go —— D-420 S2：盘口顶档深度采集装配（collect.Named 5min 节奏，与 funding
// 采集同槽；fetch=exchange 纯函数，落库+保留期清理在此适配——collect.Collector 接口
// 是 facts 形态，深度行走专表直写，MTSwapCollector 的 main 注入同族先例）。
package main

import (
	"context"
	"log"
	"strings"
	"time"

	"nox/internal/data/collect"
	"nox/internal/data/collect/exchange"
	"nox/internal/data/fact"
	"nox/internal/store/pgstore"
)

// cexDepthCollector 深度采集器（fetch→落库→保留期清理）。
// bases=DepthWhitelistBases ∪ extraBases(venue)——D-280 方案 A 分所精集：
// 动态基名=各所 funding 事实见证符号经 carryDepthBase 归一（候选腿真实集合，
// 零空打）；静态白名单对所全量保采（研究面锚定覆盖不退化）。
type cexDepthCollector struct {
	cfg        exchange.Config
	st         *pgstore.Store
	extraBases func(venue string) []string // nil=仅静态白名单
	venues     []string                    // nil=默认三所
}

func (c *cexDepthCollector) Kind() string { return "cex_depth" }

// bases 每轮快照：静态白名单 ∪ extraBases(venue)（去重保序）。
func (c *cexDepthCollector) bases(venue string) []string {
	merged := append([]string{}, exchange.DepthWhitelistBases...)
	if c.extraBases == nil {
		return merged
	}
	seen := make(map[string]bool, len(merged))
	for _, s := range merged {
		seen[s] = true
	}
	for _, b := range c.extraBases(venue) {
		if !seen[b] {
			seen[b] = true
			merged = append(merged, b)
		}
	}
	return merged
}

func (c *cexDepthCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	venues := c.venues
	if venues == nil {
		venues = []string{exchange.VenueBinance, exchange.VenueGate, exchange.VenueOKX}
	}
	var quotes []exchange.DepthQuote
	for _, venue := range venues {
		quotes = append(quotes, exchange.FetchCexDepthsVenue(ctx, c.cfg, venue, c.bases(venue))...)
	}
	if len(quotes) == 0 {
		return nil, nil
	}
	now := time.Now()
	rows := make([]pgstore.CexDepthRow, 0, len(quotes))
	for _, q := range quotes {
		rows = append(rows, pgstore.CexDepthRow{
			Venue: q.Venue, Symbol: q.Symbol, Ts: now,
			Bid1USD: q.Bid1, Ask1USD: q.Ask1, Bid1Qty: q.BidQty, Ask1Qty: q.AskQty,
			MidDepthUSD: q.MidDepthUSD(),
		})
	}
	if err := c.st.InsertCexDepth(ctx, rows); err != nil {
		return nil, err
	}
	if n, err := c.st.DeleteCexDepthBefore(ctx, now.Add(-90*24*time.Hour)); err != nil {
		log.Printf("[cex-depth] retention: %v", err) // WP1：失败留痕不阻断本轮
	} else if n > 0 {
		log.Printf("[cex-depth] retention: purged %d rows (>90d)", n)
	}
	return nil, nil
}

// buildDepthCollector D-420 S2 装配（5min 节奏与 funding 同槽；静态白名单=SSOT
// SPX500/XPT/XPD/XAU/CLU + extraBases 动态补采位，D-057 universe canonical 注入点）。
func buildDepthCollector(cfg exchange.Config, st *pgstore.Store, extraBases func(venue string) []string) collect.Named {
	return collect.Named{
		Name:     "cex_depth",
		Interval: 5 * time.Minute,
		Collector: &cexDepthCollector{
			cfg:        cfg,
			st:         st,
			extraBases: extraBases,
		},
	}
}

// fundingWitnessedDepthLister funding 见证符号读口（*pgstore.Store 实现；
// seam 供测试替身——与 carryUniverseDepthLister 同先例）。
type fundingWitnessedDepthLister interface {
	ListFundingWitnessedSymbols(ctx context.Context, venue string, window time.Duration) ([]string, error)
}

// fundingWitnessWindow funding 见证窗（6h——funding WS 流持续产帧，窗容忍断流抖
// 动；漏窗=该符号本轮不采→候选 depth_missing fail-closed 同旧语义不放大风险）。
const fundingWitnessWindow = 6 * time.Hour

// universeDepthBases D-280 方案 A：分所精集——动态基名=各所近 6h funding 见证
// 符号（symbol→carryDepthBase 基名归一去重保序）。读失败 → 仅静态白名单 + log
// （深度采集是证据面非判定面，退化不阻断本轮；异常不静默）。
func universeDepthBases(ctx context.Context, st fundingWitnessedDepthLister) func(venue string) []string {
	return func(venue string) []string {
		syms, err := st.ListFundingWitnessedSymbols(ctx, venue, fundingWitnessWindow)
		if err != nil {
			log.Printf("[cex-depth] funding witnessed bases %s: %v（本轮仅静态白名单）", venue, err)
			return nil
		}
		out := make([]string, 0, len(syms))
		seen := map[string]bool{}
		for _, sym := range syms {
			b := carryDepthBase(sym)
			if b != "" && !seen[b] {
				seen[b] = true
				out = append(out, b)
			}
		}
		log.Printf("[cex-depth] %s witnessed bases: %d", venue, len(out)) // 基名数上限观测（D-058 同口径）
		return out
	}
}

// carryDepthBase canonical → CEX 深度基名（XAUUSDT→XAU、XAU-USDT-SWAP→XAU、
// XAU_USDT→XAU；先剥 -USDT-SWAP 再剥 USDT——前者含后者子串，末清悬空分隔符）。
func carryDepthBase(canonical string) string {
	s := strings.TrimSuffix(canonical, "-USDT-SWAP")
	s = strings.TrimSuffix(s, "USDT")
	return strings.TrimRight(s, "_-")
}
