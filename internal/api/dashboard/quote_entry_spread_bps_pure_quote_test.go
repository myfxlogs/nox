// d378a_quote_anchor_test.go —— D-378a S5 锚1：XASSET 锚纯 quote 口径（先红后绿）。
// 493 病例：fill 口径 +5.316 吸收 demo 漂移成毒锚；修复后锚=纯报价（与 engine currentBasis
// 同公式，无 PriceActual 回退），fill 值只配做 exec_drift 的漂移分母（S2）。
package dashboard

import (
	"context"
	"math"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"
)

func d378aPtrS(v string) *string { return &v }

// d378aSeedXAssetPos 插入 XASSET_CARRY OPEN 仓+OPEN FILLED 双腿（fill 口径=+5.0bps：
// buy 100.05/sell 100.00——与 quote 口径 −10 分离可断言）。
func d378aSeedXAssetPos(t *testing.T, st *pgstore.Store) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx,
		`TRUNCATE positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var posID int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ('XASSET_CARRY:XAUUSD:a:b','XASSET_CARRY','OPEN',0) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	seedLeg := func(legNo int, side, venue, symbol string, price float64) {
		t.Helper()
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, price_actual, status)
			 VALUES ($1,$2,'OPEN',split_part($3,':',1), split_part($3,':',2),$4,$5,1,100,$6,'FILLED')`,
			posID, legNo, venue, symbol, side, price); err != nil {
			t.Fatalf("seed leg %s: %v", venue, err)
		}
	}
	seedLeg(1, "BUY", "277842155:mt5", "XAUUSD", 100.05)
	seedLeg(2, "SELL", "binance:perp", "PAXGUSDT", 100.00)
	return posID
}

// TestD378a_QuoteEntrySpreadBps_PureQuote 锚1a：quote 口径=(buyBid−sellAsk)/buyAsk×1e4，
// 与 fill 口径 (+5.0) 分离；写点落列后 DB 值=quote 值。
func TestD378a_QuoteEntrySpreadBps_PureQuote(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	posID := d378aSeedXAssetPos(t, st)
	qb := bus.New()
	// 买腿 quote：bid 100.00 / ask 100.02；卖腿 ask 100.10 → spread=−0.10，ref=100.02
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "277842155", VenueType: "mt5", Bid: 100.00, Ask: 100.02, Time: time.Now()})
	qb.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 100.05, Ask: 100.10, Time: time.Now()})
	s := &Service{store: st, qbus: qb}
	opp := &pgstore.Opportunity{CanonicalSell: d378aPtrS("PAXGUSDT")}

	got, ok := s.quoteEntrySpreadBps(ctx, posID, "XAUUSD", opp)
	if !ok {
		t.Fatal("锚1a: 双腿 quote 齐备应 ok=true")
	}
	want := (100.00 - 100.10) / 100.02 * 1e4 // ≈ −9.998
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("锚1a: quote anchor = %v, want %v", got, want)
	}
	if math.Abs(got-5.0) < 1.0 {
		t.Fatalf("锚1a: quote anchor %v 撞 fill 口径 +5（纯报价修复点失效）", got)
	}
	// 写点落列（IS NULL 幂等同径）→ DB 值=quote 值
	if err := st.UpdateExpectedEntrySpreadBps(ctx, posID, got); err != nil {
		t.Fatalf("write: %v", err)
	}
	rows, _ := st.ListPositions(ctx, "OPEN", 10, 0)
	if len(rows) != 1 || rows[0].ExpectedEntrySpreadBps == nil || math.Abs(*rows[0].ExpectedEntrySpreadBps-want) > 1e-6 {
		t.Fatalf("锚1a: DB 锚 = %v, want %v（quote 口径落列）", rows[0].ExpectedEntrySpreadBps, want)
	}
}

// TestD378a_QuoteAnchor_MissingQuote_NoWrite 锚1b：任一腿 quote 缺失 → ok=false 不写（NULL fail-closed）。
func TestD378a_QuoteAnchor_MissingQuote_NoWrite(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	posID := d378aSeedXAssetPos(t, st)
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "277842155", VenueType: "mt5", Bid: 100.00, Ask: 100.02, Time: time.Now()})
	// 卖腿 quote 缺失
	s := &Service{store: st, qbus: qb}
	if _, ok := s.quoteEntrySpreadBps(ctx, posID, "XAUUSD", nil); ok {
		t.Fatal("锚1b: 卖腿 quote 缺失应 ok=false（fail-closed 不写锚）")
	}
	rows, _ := st.ListPositions(ctx, "OPEN", 10, 0)
	if len(rows) != 1 || rows[0].ExpectedEntrySpreadBps != nil {
		t.Fatalf("锚1b: 缺 quote 不应写锚, got %v", rows[0].ExpectedEntrySpreadBps)
	}
}

// TestD378a_SellCanonicalFallback 锚1c：opp=nil（手动路径）→ broker_symbol_map 反查 sellLeg.Symbol。
func TestD378a_SellCanonicalFallback(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	posID := d378aSeedXAssetPos(t, st)
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES ('binance','mt5','h',443,'p') ON CONFLICT (venue) DO NOTHING; INSERT INTO broker_symbol_map (venue, broker_symbol, canonical) VALUES ('binance','PAXGUSDT','PAXGUSDT') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "277842155", VenueType: "mt5", Bid: 100.00, Ask: 100.02, Time: time.Now()})
	qb.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 100.05, Ask: 100.10, Time: time.Now()})
	s := &Service{store: st, qbus: qb}
	got, ok := s.quoteEntrySpreadBps(ctx, posID, "XAUUSD", nil)
	if !ok || math.Abs(got-((100.00-100.10)/100.02*1e4)) > 1e-6 {
		t.Fatalf("锚1c: symbol_map 反查失败: ok=%v got=%v", ok, got)
	}
}
