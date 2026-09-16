// D-300 S1 测试：asset_equiv 表 + AssetEquivProvider 缓存（DSN 真实夹具 + fake lister）。
package pgstore

import (
	"context"
	"testing"
	"time"
)

// assetEquivTestStore 复用 testStore 基建（prepareTestSchema 全量迁移含 0097 种子）。
func assetEquivTestStore(t *testing.T) *Store {
	t.Helper()
	return testStore(t)
}

// TestListAssetEquiv_SeedRows 迁移 0097 种子两行在库（裁 8：XAU venue=3001157）。
func TestListAssetEquiv_SeedRows(t *testing.T) {
	s := assetEquivTestStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE asset_equiv RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue) VALUES
		('GOLD','PAXGUSDT','perp','binance'),('GOLD','XAUUSD','mt5','3001157')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := s.ListAssetEquiv(ctx)
	if err != nil {
		t.Fatalf("ListAssetEquiv: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].AssetClass != "GOLD" {
		t.Fatalf("asset_class=%q", rows[0].AssetClass)
	}
	// enabled=false 行不出现
	if _, err := s.pool.Exec(ctx, `INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue, enabled) VALUES
		('GOLD','XAUTUSDT','perp','binance',false)`); err != nil {
		t.Fatalf("insert disabled: %v", err)
	}
	rows, err = s.ListAssetEquiv(ctx)
	if err != nil {
		t.Fatalf("ListAssetEquiv2: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("disabled row leaked: got %d rows", len(rows))
	}
}

// fakeEquivLister 供 provider 逻辑测试（免 DB）。
type fakeEquivLister struct {
	rows  []AssetEquivEntry
	calls int
}

func (f *fakeEquivLister) ListAssetEquiv(_ context.Context) ([]AssetEquivEntry, error) {
	f.calls++
	return f.rows, nil
}

// TestAssetEquivProvider_Partners 反查：同 asset_class 的其他 canonical。
func TestAssetEquivProvider_Partners(t *testing.T) {
	l := &fakeEquivLister{rows: []AssetEquivEntry{
		{AssetClass: "GOLD", Canonical: "PAXGUSDT", VenueType: "perp", Venue: "binance"},
		{AssetClass: "GOLD", Canonical: "XAUUSD", VenueType: "mt5", Venue: "3001157"},
		{AssetClass: "GOLD", Canonical: "XAUTUSDT", VenueType: "perp", Venue: "binance"},
		{AssetClass: "BTC", Canonical: "BTCUSDT", VenueType: "perp", Venue: "binance"},
		{AssetClass: "BTC", Canonical: "BTCUSD", VenueType: "mt5", Venue: "3001157"},
	}}
	p := NewAssetEquivProvider(l, time.Minute)
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	got := p.Partners("PAXGUSDT")
	if len(got) != 2 {
		t.Fatalf("PAXGUSDT partners=%v want [XAUUSD XAUTUSDT] (2)", got)
	}
	if p := p.Partners("XAUUSD"); len(p) != 2 {
		t.Fatalf("XAUUSD partners=%v want 2", p)
	}
	if p := p.Partners("ETHUSDT"); len(p) != 0 {
		t.Fatalf("unregistered canonical partners=%v want empty", p)
	}
	// 多 venue 同 canonical 不重复 partner
	if got := p.Partners("BTCUSDT"); len(got) != 1 || got[0] != "BTCUSD" {
		t.Fatalf("BTCUSDT partners=%v want [BTCUSD]", got)
	}
}

// TestAssetEquivProvider_MaybeRefreshTTL TTL 内不重查、过期重查。
func TestAssetEquivProvider_MaybeRefreshTTL(t *testing.T) {
	l := &fakeEquivLister{rows: []AssetEquivEntry{
		{AssetClass: "GOLD", Canonical: "PAXGUSDT"},
		{AssetClass: "GOLD", Canonical: "XAUUSD"},
	}}
	p := NewAssetEquivProvider(l, time.Hour)
	ctx := context.Background()
	if err := p.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	p.MaybeRefresh(ctx) // TTL 内
	if l.calls != 1 {
		t.Fatalf("calls=%d want 1 (TTL 内不重查)", l.calls)
	}
	p2 := NewAssetEquivProvider(l, time.Nanosecond)
	if err := p2.Refresh(ctx); err != nil {
		t.Fatalf("refresh2: %v", err)
	}
	time.Sleep(time.Millisecond)
	p2.MaybeRefresh(ctx) // 过期 → 重查
	if l.calls < 3 {
		t.Fatalf("calls=%d want >=3 (过期重查)", l.calls)
	}
}
