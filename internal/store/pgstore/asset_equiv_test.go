// 测试：asset_equiv 表 + AssetEquivProvider 缓存（DSN 真实夹具 + fake lister）。
package pgstore

import (
	"context"
	"testing"
	"time"

	"arb/migrations"
)

// assetEquivTestStore 复用 testStore 基建（prepareTestSchema 全量迁移含 0097 种子）。
func assetEquivTestStore(t *testing.T) *Store {
	t.Helper()
	return testStore(t)
}

// TestListAssetEquiv_SeedRows 迁移 0097 种子三行在库（裁 8：XAU venue=3001157；裁 12：XAUT 并入 0097）。
func TestListAssetEquiv_SeedRows(t *testing.T) {
	s := assetEquivTestStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE asset_equiv RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue) VALUES
		('GOLD','PAXGUSDT','perp','binance'),('GOLD','XAUTUSDT','perp','binance'),('GOLD','XAUUSD','mt5','3001157')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := s.ListAssetEquiv(ctx)
	if err != nil {
		t.Fatalf("ListAssetEquiv: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	if rows[0].AssetClass != "GOLD" {
		t.Fatalf("asset_class=%q", rows[0].AssetClass)
	}
	// enabled=false 行不出现
	if _, err := s.pool.Exec(ctx, `INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue, enabled) VALUES
		('GOLD','XAGUSD','mt5','3001157',false)`); err != nil {
		t.Fatalf("insert disabled: %v", err)
	}
	rows, err = s.ListAssetEquiv(ctx)
	if err != nil {
		t.Fatalf("ListAssetEquiv2: %v", err)
	}
	if len(rows) != 3 {
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

// TestAssetEquivProvider_PartnersDedupMultiVenue 返修 R4：同 canonical 多 venue 行时
// partners 不重复——GOLD 下 PAXGUSDT 两行（binance/okx）+XAUUSD 一行 → Partners("XAUUSD") 恰为 [PAXGUSDT]。
func TestAssetEquivProvider_PartnersDedupMultiVenue(t *testing.T) {
	l := &fakeEquivLister{rows: []AssetEquivEntry{
		{AssetClass: "GOLD", Canonical: "PAXGUSDT", VenueType: "perp", Venue: "binance"},
		{AssetClass: "GOLD", Canonical: "PAXGUSDT", VenueType: "perp", Venue: "okx"},
		{AssetClass: "GOLD", Canonical: "XAUUSD", VenueType: "mt5", Venue: "3001157"},
	}}
	p := NewAssetEquivProvider(l, time.Minute)
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := p.Partners("XAUUSD"); len(got) != 1 || got[0] != "PAXGUSDT" {
		t.Fatalf("XAUUSD partners=%v want [PAXGUSDT] len 1", got)
	}
	if got := p.Partners("PAXGUSDT"); len(got) != 1 || got[0] != "XAUUSD" {
		t.Fatalf("PAXGUSDT partners=%v want [XAUUSD] len 1", got)
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

// TestAssetEquivProvider_PartnersXauusdt D-303：0098 种子行后 GOLD 四 canonical——
// Partners("XAUUSD") 与 Partners("PAXGUSDT") 均含 XAUUSDT。
// 先锚 schema_migrations version=98 台账（防 embed/migrate 漏登记假绿，经验库
// migration-file-without-embed-registration）；表内行可能被前序测试 TRUNCATE，故种子自补。
func TestAssetEquivProvider_PartnersXauusdt(t *testing.T) {
	s := assetEquivTestStore(t)
	ctx := context.Background()
	var one int
	if err := s.pool.QueryRow(ctx, `SELECT 1 FROM schema_migrations WHERE version = 98`).Scan(&one); err != nil {
		t.Fatalf("schema_migrations version=98 missing: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue) VALUES
		('GOLD','PAXGUSDT','perp','binance'),('GOLD','XAUTUSDT','perp','binance'),
		('GOLD','XAUUSD','mt5','3001157'),('GOLD','XAUUSDT','perp','binance')
		ON CONFLICT (asset_class, canonical, venue) DO NOTHING`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p := NewAssetEquivProvider(s, time.Minute)
	if err := p.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	for _, canonical := range []string{"XAUUSD", "PAXGUSDT"} {
		got := p.Partners(canonical)
		found := false
		for _, c := range got {
			if c == "XAUUSDT" {
				found = true
			}
		}
		if !found {
			t.Fatalf("Partners(%q)=%v want contains XAUUSDT", canonical, got)
		}
	}
}

// TestMigration0100_T1Promotion D-308：迁移 0100 台账+种子行锚——
// schema_migrations version=100（防 embed/migrate 漏登记假绿，同 D-303 先例）；
// 前序测试会 TRUNCATE asset_equiv，故幂等重放迁移 SQL 再断言行（兼验文件内容可执行）。
func TestMigration0100_T1Promotion(t *testing.T) {
	s := assetEquivTestStore(t)
	ctx := context.Background()
	var one int
	if err := s.pool.QueryRow(ctx, `SELECT 1 FROM schema_migrations WHERE version = 100`).Scan(&one); err != nil {
		t.Fatalf("schema_migrations version=100 missing: %v", err)
	}
	seedBrokerVenue(t, s, "3001157") // D-496：0100 重放含 bsm 行须先有 FK 父行
	if _, err := s.pool.Exec(ctx, migrations.T1PromotionSQL); err != nil {
		t.Fatalf("replay 0100: %v", err)
	}
	// 0101 勘误重放：perp canonical 回正 <base>USDT 同键命名空间 + SILVER mt5 腿。
	if _, err := s.pool.Exec(ctx, migrations.CanonicalFixSQL); err != nil {
		t.Fatalf("replay 0101: %v", err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM asset_equiv WHERE asset_class = 'WTI'`).Scan(&n); err != nil {
		t.Fatalf("count WTI: %v", err)
	}
	if n != 4 {
		t.Fatalf("WTI class want 4 members（CLUSDT×三所/WTI）, got %d", n)
	}
	if err := s.pool.QueryRow(ctx, `SELECT 1 FROM asset_equiv WHERE asset_class='GOLD' AND canonical='XAUUSDT' AND venue='okx'`).Scan(&one); err != nil {
		t.Fatalf("GOLD okx leg missing: %v", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT 1 FROM asset_equiv WHERE asset_class='SILVER' AND canonical='SILVER' AND venue='3001157'`).Scan(&one); err != nil {
		t.Fatalf("SILVER mt5 leg missing: %v", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT 1 FROM broker_symbol_map WHERE venue='3001157' AND broker_symbol='XAGUSDm' AND canonical='SILVER'`).Scan(&one); err != nil {
		t.Fatalf("broker_symbol_map XAGUSDm missing: %v", err)
	}
}
