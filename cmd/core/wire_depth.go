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
// bases=DepthWhitelistBases ∪ extraBases()——D-057④/R1：动态基名=CFD 域
// universe 行经 asset_equiv 解析的 CEX canonical（发现层跟随，换轮自动跟采）。
type cexDepthCollector struct {
	cfg        exchange.Config
	st         *pgstore.Store
	extraBases func() []string // nil=仅静态白名单
}

func (c *cexDepthCollector) Kind() string { return "cex_depth" }

// bases 每轮快照：静态白名单 ∪ extraBases()（去重保序；extraBases nil=仅静态）。
func (c *cexDepthCollector) bases() []string {
	merged := append([]string{}, exchange.DepthWhitelistBases...)
	if c.extraBases == nil {
		return merged
	}
	seen := make(map[string]bool, len(merged))
	for _, s := range merged {
		seen[s] = true
	}
	for _, b := range c.extraBases() {
		if !seen[b] {
			seen[b] = true
			merged = append(merged, b)
		}
	}
	return merged
}

func (c *cexDepthCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	quotes := exchange.FetchCexDepths(ctx, c.cfg, c.bases())
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
func buildDepthCollector(cfg exchange.Config, st *pgstore.Store, extraBases func() []string) collect.Named {
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

// carryUniverseDepthLister universe 深度基名读口（*pgstore.Store 实现；
// seam 供测试替身——与 d053Store 同先例）。
type carryUniverseDepthLister interface {
	ListCfdUniverseDepthBases(ctx context.Context) ([]string, error)
}

// universeDepthBases D-057④/R1：深度采集动态基名源=fin_carry_universe CFD 域
// 行 canonical 经 asset_equiv perp 腿解析（SILVER→XAGUSDT 等；读口内收口）→
// carryDepthBase 剥尾去重保序。读失败 → 仅静态白名单 + log（深度采集是证据面
// 非判定面，退化不阻断本轮；异常不静默）。
func universeDepthBases(ctx context.Context, st carryUniverseDepthLister) func() []string {
	return func() []string {
		canonicals, err := st.ListCfdUniverseDepthBases(ctx)
		if err != nil {
			log.Printf("[cex-depth] universe bases read: %v（本轮仅静态白名单）", err)
			return nil
		}
		out := make([]string, 0, len(canonicals))
		seen := map[string]bool{}
		for _, c := range canonicals {
			b := carryDepthBase(c)
			if b != "" && !seen[b] {
				seen[b] = true
				out = append(out, b)
			}
		}
		log.Printf("[cex-depth] universe bases: %d", len(out)) // R1：基名数上限观测（D-058 深度闸数据面）
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
