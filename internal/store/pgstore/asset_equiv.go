package pgstore

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// AssetEquivEntry 标的等价类映射行（asset_equiv，D-300）。
// venue=QuoteBus 运行时键：CEX=所名（"binance"），MT 类=账号 ID（"3001157"，提示词裁 8）。
type AssetEquivEntry struct {
	AssetClass string
	Canonical  string
	VenueType  string
	Venue      string
}

// ListAssetEquiv 返回全部 enabled 配对行（AssetEquivProvider 的加载源）。
func (s *Store) ListAssetEquiv(ctx context.Context) ([]AssetEquivEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT asset_class, canonical, venue_type, venue
		 FROM asset_equiv WHERE enabled
		 ORDER BY asset_class, canonical, venue`)
	if err != nil {
		return nil, fmt.Errorf("list asset_equiv: %w", err)
	}
	defer rows.Close()
	var out []AssetEquivEntry
	for rows.Next() {
		var e AssetEquivEntry
		if err := rows.Scan(&e.AssetClass, &e.Canonical, &e.VenueType, &e.Venue); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AssetEquivLister 可供 AssetEquivProvider 加载的最小接口（测试可 fake）。
type AssetEquivLister interface {
	ListAssetEquiv(ctx context.Context) ([]AssetEquivEntry, error)
}

// AssetEquivProvider 标的等价类映射的运行时缓存（D-300 裁 5：单实例注入，
// engine 与 detector 共享）。消费方只读内存视图——禁 Scan 内查库；
// MaybeRefresh 由 engine.onQuote 周期驱动（TTL 内不重查）。
type AssetEquivProvider struct {
	lister AssetEquivLister
	ttl    time.Duration

	mu       sync.RWMutex
	byClass  map[string][]AssetEquivEntry // asset_class → 行
	partners map[string][]string          // canonical → 同类其他 canonical
	lastLoad time.Time
}

// NewAssetEquivProvider 建缓存；refreshTTL<=0 取 60s。
func NewAssetEquivProvider(lister AssetEquivLister, refreshTTL time.Duration) *AssetEquivProvider {
	if refreshTTL <= 0 {
		refreshTTL = 60 * time.Second
	}
	return &AssetEquivProvider{
		lister:   lister,
		ttl:      refreshTTL,
		byClass:  make(map[string][]AssetEquivEntry),
		partners: make(map[string][]string),
	}
}

// Refresh 立即重载（boot 首载+测试用）。
func (p *AssetEquivProvider) Refresh(ctx context.Context) error {
	rows, err := p.lister.ListAssetEquiv(ctx)
	if err != nil {
		return err
	}
	byClass := make(map[string][]AssetEquivEntry)
	partners := make(map[string][]string)
	for _, e := range rows {
		byClass[e.AssetClass] = append(byClass[e.AssetClass], e)
	}
	for _, entries := range byClass {
		seen := make(map[string]bool, len(entries))
		canonicals := make([]string, 0, len(entries))
		for _, e := range entries {
			if !seen[e.Canonical] {
				seen[e.Canonical] = true
				canonicals = append(canonicals, e.Canonical)
			}
		}
		for _, e := range entries {
			for _, c := range canonicals {
				if c != e.Canonical {
					partners[e.Canonical] = append(partners[e.Canonical], c)
				}
			}
		}
	}
	p.mu.Lock()
	p.byClass = byClass
	p.partners = partners
	p.lastLoad = time.Now()
	p.mu.Unlock()
	return nil
}

// MaybeRefresh TTL 过期才重载；失败保留旧视图打日志（detector 路径不中断）。
func (p *AssetEquivProvider) MaybeRefresh(ctx context.Context) {
	p.mu.RLock()
	stale := time.Since(p.lastLoad) > p.ttl
	p.mu.RUnlock()
	if !stale {
		return
	}
	if err := p.Refresh(ctx); err != nil {
		log.Printf("[asset_equiv] refresh failed (keep stale view): %v", err)
	}
}

// ByClass 返回 asset_class → enabled 行（detector 消费；深拷贝防外部改）。
func (p *AssetEquivProvider) ByClass() map[string][]AssetEquivEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string][]AssetEquivEntry, len(p.byClass))
	for k, v := range p.byClass {
		cp := make([]AssetEquivEntry, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// Partners 反查：canonical → 同 asset_class 的其他 canonical 列表（engine 快照扩展用）。
// 未注册 canonical → nil（无伙伴）。
func (p *AssetEquivProvider) Partners(canonical string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, len(p.partners[canonical]))
	copy(out, p.partners[canonical])
	return out
}
