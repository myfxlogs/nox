package scan

// classkey_open_gate_test.go —— 类键行开仓解析闸锚：歧义类键（binance GOLD=
// PAXG/XAUT/XAU 三 perp 各 funding 异）→拒开不落册+warn；唯一类键
// （SILVER@okx 单 perp）→照常开。判据与 accrue 探针同源复用断言。

import (
	"context"
	"testing"
	"time"
)

func TestClassKeyOpenGate(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue, enabled) VALUES
		 ('GOLD','XAUUSD','mt5','15032313',true),('GOLD','PAXGUSDT','perp','binance',true),
		 ('GOLD','XAUTUSDT','perp','binance',true),('GOLD','XAUUSDT','perp','binance',true),
		 ('SILVER','XAGUSD','mt5','15032313',true),('SILVER','XAGUSDT','perp','okx',true)
		 ON CONFLICT DO NOTHING`,
		`INSERT INTO fin_cex_universe (venue, symbol, base, status, hedge_canonical) VALUES
		 ('binance','PAXGUSDT','PAXG','TRADING','XAUUSD'),('binance','XAUTUSDT','XAUT','TRADING','XAUUSD'),
		 ('binance','XAUUSDT','XAU','TRADING','XAUUSD'),('okx','XAG-USDT-SWAP','XAG','TRADING','XAGUSD')`,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES
		 ('15032313','mt5','h',443,'p') ON CONFLICT (venue) DO NOTHING`,
		`INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, is_primary) VALUES
		 ('15032313','XAUUSDm','XAUUSD',true),('15032313','XAGUSDm','XAGUSD',true)
		 ON CONFLICT DO NOTHING`,
	} {
		if _, err := s.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM asset_equiv WHERE asset_class IN ('GOLD','SILVER')`,
			`DELETE FROM fin_cex_universe WHERE symbol IN ('PAXGUSDT','XAUTUSDT','XAUUSDT','XAG-USDT-SWAP')`,
			`DELETE FROM broker_symbol_map WHERE venue='15032313' AND canonical IN ('XAUUSD','XAGUSD')`,
			`DELETE FROM broker_accounts WHERE venue='15032313' AND host='h'`,
			`DELETE FROM paper_spot_carry WHERE symbol IN ('GOLD','SILVER')`,
			`DELETE FROM alerts WHERE message LIKE '%类键行拒开仓%'`,
		} {
			_, _ = s.Pool().Exec(context.Background(), q)
		}
	})
	equiv, err := s.ListAssetEquiv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	view := newAssetClassView(equiv)
	// 歧义类键：GOLD@binance（三 perp）→ 拒开+warn，零落册。
	err = insertCfdPaper(ctx, s, UniverseRow{Canonical: "GOLD", MtVenue: "15032313",
		CexVenue: "binance", Structure: "short_cfd_long_perp", NetAnnual: 5.0},
		"15032313", "binance", view, time.Now())
	if err != nil {
		t.Fatalf("歧义类键拒开应 nil err（warn 如实记）: %v", err)
	}
	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM paper_spot_carry WHERE symbol='GOLD'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("歧义类键不应落册: %d", n)
	}
	var warns int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message LIKE '%类键行拒开仓%' AND message LIKE '%GOLD%'`).Scan(&warns); err != nil {
		t.Fatal(err)
	}
	if warns == 0 {
		t.Fatal("歧义拒开应 warn 留痕")
	}
	// 唯一类键：SILVER@okx（单 perp 唯一绑定）→ 照常开。
	err = insertCfdPaper(ctx, s, UniverseRow{Canonical: "SILVER", MtVenue: "15032313",
		CexVenue: "okx", Structure: "short_cfd_long_perp", NetAnnual: 5.0},
		"15032313", "okx", view, time.Now())
	if err != nil {
		t.Fatalf("唯一类键开仓 err: %v", err)
	}
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM paper_spot_carry WHERE symbol='SILVER'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("唯一类键应开 1 行: %d", n)
	}
}

// TestClassKeyOpenGate_NoMtLeg 退回补正③：类键行无 MT 注册腿（cexByCls 有类
// 而 mtByCls 空）——触发键双侧罩判后闸仍触发，双腿不可解→拒开+warn 不落册。
func TestClassKeyOpenGate_NoMtLeg(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	// OIL 类：cex 侧单腿注册（唯一绑定可解），MT 侧无注册腿。
	for _, q := range []string{
		`INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue, enabled) VALUES
		 ('OIL','USOILUSDT','perp','binance',true) ON CONFLICT DO NOTHING`,
		`INSERT INTO fin_cex_universe (venue, symbol, base, status, hedge_canonical) VALUES
		 ('binance','USOILUSDT','USOIL','TRADING','USOILUSD')`,
	} {
		if _, err := s.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM asset_equiv WHERE asset_class='OIL'`,
			`DELETE FROM fin_cex_universe WHERE symbol='USOILUSDT'`,
			`DELETE FROM paper_spot_carry WHERE symbol='OIL'`,
			`DELETE FROM alerts WHERE message LIKE '%类键行拒开仓%' AND message LIKE '%OIL%'`,
		} {
			_, _ = s.Pool().Exec(context.Background(), q)
		}
	})
	equiv, err := s.ListAssetEquiv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	view := newAssetClassView(equiv)
	err = insertCfdPaper(ctx, s, UniverseRow{Canonical: "OIL", MtVenue: "15032313",
		CexVenue: "binance", Structure: "short_cfd_long_perp", NetAnnual: 5.0},
		"15032313", "binance", view, time.Now())
	if err != nil {
		t.Fatalf("拒开应 nil err（warn 如实记）: %v", err)
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM paper_spot_carry WHERE symbol='OIL'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("无 MT 腿类键行不应落册: %d", n)
	}
	var warns int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE message LIKE '%类键行拒开仓%' AND message LIKE '%OIL%'`).Scan(&warns); err != nil {
		t.Fatal(err)
	}
	if warns == 0 {
		t.Fatal("无 MT 腿拒开应 warn 留痕")
	}
}
