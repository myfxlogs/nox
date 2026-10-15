package main

// 日历基差 P0 纸面轨锚测：配对域/开仓行/T−1 退/Δbasis 重估/滑点缺降级。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"nox/internal/platform/testutil/storetest"
)

type fakeDeliverySrc struct{ spreads map[string]float64 }

func (f fakeDeliverySrc) Venue() string                       { return "binance" }
func (f fakeDeliverySrc) Scan(context.Context) ([]Row, error) { return nil, nil }
func (f fakeDeliverySrc) DeliverySpreadBps(_ context.Context, sym string) (float64, error) {
	v, ok := f.spreads[sym]
	if !ok {
		return 0, fmt.Errorf("no spread")
	}
	return v, nil
}

func deliveryTestRows(now time.Time) []normRow {
	exp := now.Add(100 * 24 * time.Hour)
	return []normRow{
		{Venue: "binance", Canonical: "BTC",
			Row: Row{Symbol: "BTCUSDT", ContractType: ctPerp, FundingRate: 0.0001, IntervalH: 8}},
		{Venue: "binance", Canonical: "BTC",
			Row: Row{Symbol: "BTCUSDT_261225", ContractType: ctFutures, Mark: 101000, Index: 100000, ExpiryTS: exp}},
		// okx USD_UM 行（D-525 go：USDⓈ 结算线性交割）+ 同所 perp → 应入配对
		{Venue: "okx", Canonical: "SOL",
			Row: Row{Symbol: "SOL-USD_UM-261030", ContractType: ctFutures, Mark: 210, Index: 200, ExpiryTS: exp}},
		{Venue: "okx", Canonical: "SOL",
			Row: Row{Symbol: "SOL-USDT-SWAP", ContractType: ctPerp}},
		// okx USD_UM 无同所 perp 腿（跨所配对禁建——binance perp 不算腿）
		{Venue: "okx", Canonical: "ETH",
			Row: Row{Symbol: "ETH-USD_UM-261030", ContractType: ctFutures, Mark: 4100, Index: 4000, ExpiryTS: exp}},
		{Venue: "binance", Canonical: "ETH",
			Row: Row{Symbol: "ETHUSDT", ContractType: ctPerp}},
		// 负基差 backwardation P0 不收
		{Venue: "binance", Canonical: "XAUUSD",
			Row: Row{Symbol: "XAUUSDT_261225", ContractType: ctFutures, Mark: 9900, Index: 10000, ExpiryTS: exp}},
		{Venue: "binance", Canonical: "XAUUSD",
			Row: Row{Symbol: "XAUUSDT", ContractType: ctPerp}},
	}
}

// TestDeliveryPairs 配对域：binance 同 canonical perp×futures 正基差才入对。
func TestDeliveryPairs(t *testing.T) {
	now := time.Now()
	pairs := deliveryPairs(deliveryTestRows(now), map[string]*float64{}, now)
	if len(pairs) != 2 {
		t.Fatalf("P0 配对域破（want 2 行——binance BTC + okx SOL_UM）：%+v", pairs)
	}
	bySym := map[string]calPairWithVenue{}
	for _, p := range pairs {
		bySym[p.FutSym] = p
	}
	if p, ok := bySym["BTCUSDT_261225"]; !ok || p.Venue != "binance" ||
		p.BasisFrac < 0.009 || p.BasisFrac > 0.011 {
		t.Fatalf("binance 对破：%+v", p)
	}
	p, ok := bySym["SOL-USD_UM-261030"]
	if !ok || p.Venue != "okx" || p.BasisFrac < 0.049 || p.BasisFrac > 0.051 {
		t.Fatalf("okx USD_UM 对破：%+v", p)
	}
}

// TestDeliveryStep 全链：开仓（structure/腿型/价差 fact+滑点未知标）→Δbasis 重估→T−1 退。
func TestDeliveryStep(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	now := time.Now()
	srcs := []Source{fakeDeliverySrc{spreads: map[string]float64{}}}
	rows := deliveryTestRows(now)
	// 共享库清场：fixture 符号历史行退避（幂等——跑过多轮的残留）。
	if _, err := st.Pool().Exec(ctx, `DELETE FROM paper_spot_carry
		WHERE symbol IN ('BTCUSDT_261225','SOL-USD_UM-261030','ETH-USD_UM-261030','XAUUSDT_261225')`); err != nil {
		t.Fatal(err)
	}

	// 开仓：滑点源返回空 map→BTCUSDT_261225 行标「滑点未知」
	opened, _, err := stepPaperDelivery(ctx, st, srcs, rows, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened) != 2 {
		t.Fatalf("开仓破：%v（want 2——binance BTC + okx SOL_UM）", opened)
	}
	var struct_, vta, vtb, note string
	var entryBps float64
	if err := st.Pool().QueryRow(ctx,
		`SELECT structure, venue_type_a, venue_type_b, note, entry_basis_bps
		 FROM paper_spot_carry WHERE symbol='BTCUSDT_261225'`).
		Scan(&struct_, &vta, &vtb, &note, &entryBps); err != nil {
		t.Fatalf("纸面行未落: %v", err)
	}
	if struct_ != "perp_delivery" || vta != "futures" || vtb != "spot" {
		t.Fatalf("行结构破：%s %s %s", struct_, vta, vtb)
	}
	if note != "滑点未知" {
		t.Fatalf("滑点缺应标未知：%q", note)
	}
	if entryBps < 99 || entryBps > 101 {
		t.Fatalf("entry basis bps 破：%v", entryBps)
	}

	// Δbasis 重估：基差收敛到 +50bps → mtm=(100−50)×1000/1e4=5.0
	rows2 := deliveryTestRows(now)
	rows2[1].Mark = 100500
	if _, _, err := stepPaperDelivery(ctx, st, srcs, rows2, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var accrued float64
	if err := st.Pool().QueryRow(ctx,
		`SELECT accrued_usd FROM paper_spot_carry WHERE symbol='BTCUSDT_261225'`).Scan(&accrued); err != nil {
		t.Fatal(err)
	}
	if accrued < 4.9 || accrued > 5.1 {
		t.Fatalf("Δbasis 重估破：%v", accrued)
	}

	// T−1：expiry 推到 12h 内 → expiry_t_minus_1 退
	rows3 := deliveryTestRows(now)
	rows3[1].ExpiryTS = now.Add(12 * time.Hour)
	_, exited, err := stepPaperDelivery(ctx, st, srcs, rows3, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if exited != 1 {
		t.Fatalf("T−1 退破：%d", exited)
	}
	var reason string
	if err := st.Pool().QueryRow(ctx,
		`SELECT exit_reason FROM paper_spot_carry WHERE symbol='BTCUSDT_261225'`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "expiry_t_minus_1" {
		t.Fatalf("退因破：%s", reason)
	}
	if _, err := st.Pool().Exec(ctx, `DELETE FROM paper_spot_carry WHERE symbol='BTCUSDT_261225'`); err != nil {
		t.Fatal(err)
	}
}
