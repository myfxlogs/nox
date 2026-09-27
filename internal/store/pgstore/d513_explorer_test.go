// d513_explorer_test.go —— D-513 S3 查询层验收：离散度口径/闲置标记/state 幂等/提案 dedupe。
// 需 ARB_TEST_PG_DSN（_test 库，逐包隔离）。
package pgstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"arb/internal/testutil"
)

func d513Store(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	s, err := New(ctx, testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore new: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, tbl := range []string{"facts", "fin_venue_account_snapshot", "income_events",
		"explorer_proposals", "explorer_state", "broker_symbol_map", "fin_cex_universe"} {
		if _, err := s.pool.Exec(ctx, "DELETE FROM "+tbl); err != nil {
			t.Fatalf("clean %s: %v", tbl, err)
		}
	}
	return s, ctx
}

func TestD513_TermDispersion(t *testing.T) {
	s, ctx := d513Store(t)
	now := time.Now()
	seed := func(kind, venue, sym string, v float64, ts time.Time) {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO facts (kind,venue,symbol,value,unit,ts,src) VALUES ($1,$2,$3,$4,'u',$5,'d513test')`,
			kind, venue, sym, v, ts); err != nil {
			t.Fatalf("seed fact: %v", err)
		}
	}
	// ETH funding 三 venue 离散 + 一 7d 前值；BTC 单 venue。
	for _, x := range []struct {
		v   string
		val float64
	}{{"binance", 0.01}, {"okx", 0.02}, {"gate", 0.03}} {
		seed("funding", x.v, "ETHUSDT", x.val, now)
	}
	seed("funding", "binance", "BTCUSDT", 0.005, now)
	seed("funding", "binance", "ETHUSDT", 0.008, now.Add(-8*24*time.Hour)) // 7d 前值
	// 陈旧行（同日更早 ts）不应成为“最新”。
	seed("funding", "binance", "ETHUSDT", 0.009, now.Add(-time.Hour))
	// MT swap 经 broker_symbol_map canonical 归一。
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO broker_symbol_map (venue,broker_symbol,canonical) VALUES ('3001157','XAUUSD.m','XAUUSD')`); err != nil {
		t.Fatalf("seed map: %v", err)
	}
	seed("swap_long", "3001157", "XAUUSD.m", -1.5, now)
	seed("swap_long", "39050818", "XAUUSD", -2.0, now) // 无 map 行→裸 symbol 兜底 canonical

	out, err := s.ExplorerTermDispersion(ctx, now)
	if err != nil {
		t.Fatalf("dispersion: %v", err)
	}
	var eth, btc, xau *ExplorerDispersion
	for i := range out {
		d := &out[i]
		switch {
		case d.Kind == "funding" && d.Canonical == "ETHUSDT":
			eth = d
		case d.Kind == "funding" && d.Canonical == "BTCUSDT":
			btc = d
		case d.Kind == "swap_long" && d.Canonical == "XAUUSD":
			xau = d // map 行与裸 symbol 行殊途同归同一 canonical
		}
	}
	if eth == nil || eth.Venues != 3 || !d513approx(eth.Min, 0.01) || !d513approx(eth.Max, 0.03) || !d513approx(eth.Range, 0.02) {
		t.Fatalf("eth dispersion wrong: %+v", eth)
	}
	if !eth.HasPrev || !d513approx(eth.PrevMean, 0.008) {
		t.Fatalf("eth prev mean wrong: %+v", eth) // 7d 前只有 binance 0.008
	}
	if btc == nil || btc.Venues != 1 || btc.Range != 0 || btc.HasPrev {
		t.Fatalf("btc single-venue wrong: %+v", btc)
	}
	if xau == nil || xau.Venues != 2 || xau.Min != -2.0 {
		t.Fatalf("xau canonical merge wrong: %+v", xau)
	}
}

func d513approx(a, b float64) bool { return a > b-1e-9 && a < b+1e-9 }

func TestD513_IdleFunds(t *testing.T) {
	s, ctx := d513Store(t)
	now := time.Now()
	if _, err := s.pool.Exec(ctx, `INSERT INTO fin_venue_account_snapshot
		(venue,venue_type,balance_raw,equity_raw,free_margin_raw,currency,buffer_multiple) VALUES
		('binance','spot',1000,1000,800,'USDT',1.5),
		('gate','perp',500,500,400,'USDT',0.8),
		('okx','unified',0,0,0,'USDT',NULL)`); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO income_events
		(venue,symbol,income_type,amount_usd,tran_id,ts) VALUES
		('binance','ETHUSDT','FUNDING',12.5,1,$1)`, now.Add(-2*24*time.Hour)); err != nil {
		t.Fatalf("seed income: %v", err)
	}
	out, err := s.ExplorerIdleFunds(ctx, now)
	if err != nil {
		t.Fatalf("idle funds: %v", err)
	}
	by := map[string]ExplorerIdlePool{}
	for _, p := range out {
		by[p.Venue+":"+p.VenueType] = p
	}
	b := by["binance:spot"]
	if !d513approx(b.Income30dUSD, 12.5) || b.Idle || b.BelowFloor {
		t.Fatalf("binance flags wrong: %+v", b)
	}
	g := by["gate:perp"]
	if !g.Idle || !g.BelowFloor {
		t.Fatalf("gate flags wrong: %+v", g) // 余额>0 且零收入=闲置；0.8<1=破地板
	}
	o := by["okx:unified"]
	if o.Idle || o.BelowFloor {
		t.Fatalf("okx flags wrong: %+v", o) // 零余额不算闲置；NULL buffer 不破地板
	}
}

func TestD513_StateAndProposalDedupe(t *testing.T) {
	s, ctx := d513Store(t)
	if _, ok, err := s.ExplorerStateGet(ctx, "d513_missing"); err != nil || ok {
		t.Fatalf("missing key should be ok=false: %v %v", ok, err)
	}
	if err := s.ExplorerStatePut(ctx, "d513_k", json.RawMessage(`{"a":1}`)); err != nil {
		t.Fatalf("state put: %v", err)
	}
	if err := s.ExplorerStatePut(ctx, "d513_k", json.RawMessage(`{"a":2}`)); err != nil {
		t.Fatalf("state put2: %v", err)
	}
	v, ok, err := s.ExplorerStateGet(ctx, "d513_k")
	var got map[string]int
	if !ok || err != nil || json.Unmarshal(v, &got) != nil || got["a"] != 2 {
		t.Fatalf("state overwrite: %s %v %v", v, ok, err)
	}
	day := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	ps := []ExplorerProposal{
		{ScanDate: day, ScanKind: "daily", Canonical: "ETHUSDT", BestPair: "binance×okx", Direction: "watch", Rationale: "r"},
		{ScanDate: day, ScanKind: "daily", Canonical: "BTCUSDT", Direction: "watch"},
	}
	n, err := s.ExplorerProposalInsert(ctx, ps)
	if err != nil || n != 2 {
		t.Fatalf("insert n=%d err=%v", n, err)
	}
	n2, err := s.ExplorerProposalInsert(ctx, ps) // 同日重跑→dedupe 索引全跳过
	if err != nil || n2 != 0 {
		t.Fatalf("dedupe insert n=%d err=%v", n2, err)
	}
	var cnt int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM explorer_proposals`).Scan(&cnt); err != nil || cnt != 2 {
		t.Fatalf("rows=%d err=%v", cnt, err)
	}
}
