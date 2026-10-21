// wire_graduation.go D-403 S4：detector universe-hint 配对源装配——fin_carry_universe
// （cfd×perp 且 note=”）× broker_accounts（platform）× broker_symbol_map 经
// scan.ResolveGraduationCanonicals 单源解析成 CfdPairHint。映射缺失行 skip+log 计数
// （fail-closed 不臆造配对）。TTL 缓存同 AssetEquivProvider 惯例（scan tick 热路径防每轮查库；
// universe 4h 步进+映射人工/automap 变更，15min 陈旧窗足可接受）。
package main

import (
	"context"
	"log"
	"sync"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/research/detector"
	"nox/internal/research/scan"
	"nox/internal/store/pgstore"
)

type cfdHintSource struct {
	store  *pgstore.Store
	ttl    time.Duration
	mu     sync.Mutex
	hints  []detector.CfdPairHint
	loaded time.Time
}

func newCfdHintSource(st *pgstore.Store) *cfdHintSource {
	return &cfdHintSource{store: st, ttl: 15 * time.Minute}
}

// CfdPairs 提示读取（TTL 缓存；重载失败保持上次快照——hint 缺席只减候选零风险，
// fail-open 同 AssetEquivProvider 惯例；首载失败=空 hint 集）。
func (c *cfdHintSource) CfdPairs(ctx context.Context) []detector.CfdPairHint {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded.IsZero() && time.Since(c.loaded) < c.ttl {
		return c.hints
	}
	hints, err := c.load(ctx)
	if err != nil {
		log.Printf("[graduation] cfd hint load: %v（保持上次快照，hint 缺席=零候选零风险）", err)
		return c.hints
	}
	c.hints, c.loaded = hints, time.Now()
	return c.hints
}

func (c *cfdHintSource) load(ctx context.Context) ([]detector.CfdPairHint, error) {
	uni, err := c.store.ListCfdCarryUniverseClean(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := c.store.ListBrokerAccounts(ctx)
	if err != nil {
		return nil, err
	}
	platform := make(map[string]int, len(accounts))
	for _, a := range accounts {
		platform[a.Venue] = a.Platform
	}
	equiv, eqErr := c.store.ListAssetEquiv(ctx)
	if eqErr != nil { // D-043：equiv 读失败降级 legacy（class 键行 resolver 自会 fail-closed）
		log.Printf("[graduation] asset_equiv read: %v（降级 legacy 解析）", eqErr)
	}
	skipped := 0
	seen := make(map[string]bool)
	out := make([]detector.CfdPairHint, 0, len(uni))
	for _, r := range uni {
		mappings, err := c.store.ListSymbolMappings(ctx, r.MtVenue)
		if err != nil {
			skipped++
			continue
		}
		entries := make([]scan.SymbolMapEntry, 0, len(mappings))
		for _, m := range mappings {
			entries = append(entries, scan.SymbolMapEntry{Canonical: m.Canonical, IsPrimary: m.IsPrimary})
		}
		pair, err := scan.ResolveGraduationCanonicals(r.Canonical, r.MtVenue, r.CexVenue, platform[r.MtVenue], entries, equiv)
		if err != nil {
			skipped++ // 映射缺失 fail-closed：不臆造配对（universe 行在而 map 不在=链路未通）
			continue
		}
		h := detector.CfdPairHint{
			MtCanonical:  pair.MtCanonical,
			CexCanonical: pair.CexCanonical,
			MtVenueKey:   pair.MtVenueKey,
			CexVenueKey:  keydomain.Join(r.CexVenue, "perp"),
		}
		key := h.MtCanonical + "|" + h.CexCanonical + "|" + h.MtVenueKey + "|" + h.CexVenueKey
		if seen[key] {
			continue // 双 structure 行同配对去重（long/short 各一行不重复产 hint）
		}
		seen[key] = true
		out = append(out, h)
	}
	if skipped > 0 {
		log.Printf("[graduation] cfd hints: rows=%d hints=%d skipped=%d（映射缺失 fail-closed）", len(uni), len(out), skipped)
	}
	return out, nil
}

// armedDeliveryHintSource D-649续③：armed 交割配对提示源——xasset_allow_combos
// 含 futures 腿行枚举 DeliveryPairHint（perp_delivery 机会断供修：毕业批放行≠
// 发现层扫）。tenor=universe.expiry_ts−now（年化分母；过期/缺=行剔除 fail-closed）。
// TTL 缓存同 cfdHintSource。
type armedDeliveryHintSource struct {
	store  *pgstore.Store
	ttl    time.Duration
	mu     sync.Mutex
	hints  []detector.DeliveryPairHint
	loaded time.Time
}

func newArmedDeliveryHintSource(st *pgstore.Store) *armedDeliveryHintSource {
	return &armedDeliveryHintSource{store: st, ttl: 15 * time.Minute}
}

func (c *armedDeliveryHintSource) DeliveryPairs(ctx context.Context) []detector.DeliveryPairHint {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded.IsZero() && time.Since(c.loaded) < c.ttl {
		return c.hints
	}
	hints, err := c.load(ctx)
	if err != nil {
		log.Printf("[graduation] delivery hint load: %v（保持上次快照）", err)
		return c.hints
	}
	c.hints, c.loaded = hints, time.Now()
	return c.hints
}

func (c *armedDeliveryHintSource) load(ctx context.Context) ([]detector.DeliveryPairHint, error) {
	rows, err := c.store.ListArmedDeliveryPairs(ctx)
	if err != nil {
		return nil, err
	}
	var out []detector.DeliveryPairHint
	for _, r := range rows {
		h := detector.DeliveryPairHint{
			BuyCanonical: r.BuyCanonical, SellCanonical: r.SellCanonical,
			BuyVenueKey: r.BuyVenueKey, SellVenueKey: r.SellVenueKey,
		}
		if r.BuyVT == "futures" {
			if r.BuyExpiry == nil {
				log.Printf("[graduation] delivery hint %s expiry 缺（universe 无 FUTURES 行）——剔除", r.BuyCanonical)
				continue
			}
			h.BuyTenorDays = time.Until(*r.BuyExpiry).Hours() / 24
		}
		if r.SellVT == "futures" {
			if r.SellExpiry == nil {
				log.Printf("[graduation] delivery hint %s expiry 缺——剔除", r.SellCanonical)
				continue
			}
			h.SellTenorDays = time.Until(*r.SellExpiry).Hours() / 24
		}
		if (r.BuyVT == "futures" && h.BuyTenorDays <= 0) || (r.SellVT == "futures" && h.SellTenorDays <= 0) {
			continue // 已过期=tenor 非正剔除（年化分母不可负）
		}
		out = append(out, h)
	}
	return out, nil
}
