package main

// D-335R1 T1/T2：monitor Exness 腿参数化——每对取各自 exSym 报价；
// 无新鲜 tick 逐对 skip（禁止任何腿顶替）；量级卫生断言 |esp|<1000bps。
import (
	"context"
	"testing"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"

	"github.com/jackc/pgx/v5/pgxpool"
)

func d335r1Store(t *testing.T) *pgstore.Store {
	t.Helper()
	s, err := pgstore.New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `TRUNCATE fin_xasset_monitor, ticks`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

func d335r1SeedTick(t *testing.T, s *pgstore.Store, venue, symbol string, bid, ask float64) {
	t.Helper()
	_, err := s.Pool().Exec(context.Background(),
		`INSERT INTO ticks (ts, venue, venue_type, symbol, bid, ask) VALUES (now(), $1, 'mt5', $2, $3, $4)`, venue, symbol, bid, ask)
	if err != nil {
		t.Fatalf("seed tick %s/%s: %v", venue, symbol, err)
	}
}

// d335r1StubFetches 隔离 cex-proxy/公网：binance 价 stub+fundingInfo stub+okx stub
// （真实现会发外网请求；D-423 okx 路由同 seam 隔离）。
func d335r1StubFetches(t *testing.T, bid, ask float64) {
	t.Helper()
	origGold, origOkx, origGate := fetchBinanceGoldFn, fetchOkxGoldFn, fetchGateGoldFn
	gold := func(context.Context, *pgxpool.Pool, string) (float64, float64, float64, int, error) {
		return bid, ask, 0.0001, 3, nil
	}
	fetchBinanceGoldFn, fetchOkxGoldFn, fetchGateGoldFn = gold, gold, gold
	t.Cleanup(func() {
		fetchBinanceGoldFn, fetchOkxGoldFn, fetchGateGoldFn = origGold, origOkx, origGate
	})
}

func d335r1Count(t *testing.T, s *pgstore.Store, pair string) int {
	t.Helper()
	var n int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM fin_xasset_monitor WHERE pair=$1 AND cex_venue='binance:perp'`, pair).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", pair, err)
	}
	return n
}

// TestD335R1_NoTickSkipsPair T1 集成锚：ticks 只 seed XAUUSD（无 BTCUSDT）→ runPairs 后
// BTCUSDT-BTC 整轮 skip 不落行（红=去参数化回共享 XAU 腿 → BTC 对执行落垃圾行）；
// T3 金对三行回归不变。
func TestD335R1_NoTickSkipsPair(t *testing.T) {
	s := d335r1Store(t)
	d335r1SeedTick(t, s, "3001157", "XAUUSD", 4354, 4355)
	d335r1StubFetches(t, 4360, 4361) // binance 金价形态
	if err := runPairs(context.Background(), s, "3001157", derivedPairs()); err != nil {
		t.Fatalf("runPairs: %v", err)
	}
	if n := d335r1Count(t, s, "BTCUSDT-BTC"); n != 0 {
		t.Errorf("BTC 对无 Exness 腿应整轮 skip 不落行（红=顶替腿落垃圾行）, got %d 行", n)
	}
	for _, pair := range []string{"PAXG-XAU", "XAUT-XAU", "XAUUSDT-XAU"} {
		if n := d335r1Count(t, s, pair); n != 1 {
			t.Errorf("T3 金对 %s 回归: want 1 行, got %d", pair, n)
		}
	}
}

// TestD335R1_BTCTickParity T2 集成锚：seed BTCUSDT tick 100000 + binance 100050 →
// BTC 对落行 esp≈5bps（量级卫生断言 |esp|<1000bps；红=顶替腿形态 175348bps）。
func TestD335R1_BTCTickParity(t *testing.T) {
	s := d335r1Store(t)
	d335r1SeedTick(t, s, "3001157", "BTCUSDT", 100000, 100000)
	d335r1StubFetches(t, 100049, 100050)
	if err := runPairs(context.Background(), s, "3001157", derivedPairs()); err != nil {
		t.Fatalf("runPairs: %v", err)
	}
	var esp float64
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT entry_spread_bps FROM fin_xasset_monitor WHERE pair='BTCUSDT-BTC' ORDER BY ts DESC LIMIT 1`).Scan(&esp); err != nil {
		t.Fatalf("BTC 行应落库: %v", err)
	}
	if esp <= -1000 || esp >= 1000 {
		t.Errorf("T2 esp=%.2fbps 超卫生带 |esp|<1000（红=XAU 顶替=175348 形态）", esp)
	}
	if esp < 0 || esp > 10 {
		t.Errorf("T2 esp=%.2fbps want ≈5（100050 vs 100000）", esp)
	}
}

func TestD335R1_TickFreshness(t *testing.T) {
	s := d335r1Store(t)
	// 陈旧 tick（25min 前）→ skip（>10min）
	d335r1SeedTick(t, s, "3001157", "XAUUSD", 4354, 4355)
	if _, err := s.Pool().Exec(context.Background(),
		`UPDATE ticks SET ts = now()-interval '25 minutes' WHERE venue='3001157'`); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if _, _, err := latestExnessTick(context.Background(), s.Pool(), "3001157", "XAUUSD"); err == nil {
		t.Error("陈旧 tick（>10min）应报错 skip，got nil")
	}
}

// TestD423_OkxVenueRow D-423 S1 锚：okx:perp 路由行落库带 venue 三元组
// （cex_venue='okx:perp'/mt_venue='3001157:mt5'），与 binance 簿同 pair 隔离；
// 且 fetchOkxGoldFn 被调（路由按 cexVenue 不走 binance fetch，D-591 S2 读库化）。
func TestD423_OkxVenueRow(t *testing.T) {
	s := d335r1Store(t)
	d335r1SeedTick(t, s, "3001157", "XAUUSD", 4354, 4355)
	d335r1StubFetches(t, 4360, 4361)
	okxCalled := false
	orig := fetchOkxGoldFn
	fetchOkxGoldFn = func(context.Context, *pgxpool.Pool, string) (float64, float64, float64, int, error) {
		okxCalled = true
		return 4360, 4361, 0.0001, 3, nil
	}
	t.Cleanup(func() { fetchOkxGoldFn = orig })
	if err := runPairs(context.Background(), s, "3001157", derivedPairs()); err != nil {
		t.Fatalf("runPairs: %v", err)
	}
	if !okxCalled {
		t.Error("okx 行应走 fetchOkxTicker 路由")
	}
	var cex, mt string
	var n int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT cex_venue, mt_venue, count(*) OVER () FROM fin_xasset_monitor
		 WHERE pair='XAUUSDT-XAU' AND cex_venue='okx:perp'`).Scan(&cex, &mt, &n); err != nil {
		t.Fatalf("okx venue 行应落库: %v", err)
	}
	if cex != "okx:perp" || mt != "3001157:mt5" {
		t.Errorf("venue 三元组 want okx:perp×3001157:mt5, got %s×%s", cex, mt)
	}
	// 同 pair binance 簿行隔离不混：cex_venue='binance:perp' 行另计 1。
	var bn int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM fin_xasset_monitor WHERE pair='XAUUSDT-XAU' AND cex_venue='binance:perp'`).Scan(&bn); err != nil || bn != 1 {
		t.Errorf("binance 簿同 pair 行应独立存在 n=%d err=%v", bn, err)
	}
}

func TestD335R1_EntrySpreadMagnitude(t *testing.T) {
	s := d335r1Store(t)
	// T2：BTCUSDT tick 价 100000 + binance 100050 → esp=(100050-100000)/100000*1e4=5bps（量级卫生）。
	d335r1SeedTick(t, s, "3001157", "BTCUSDT", 100000, 100000)
	bid, ask, err := latestExnessTick(context.Background(), s.Pool(), "3001157", "BTCUSDT")
	if err != nil || bid != 100000 || ask != 100000 {
		t.Fatalf("BTCUSDT tick: %v/%v/%v", bid, ask, err)
	}
	esp := (100050 - ask) / ask * 10000
	if esp <= -1000 || esp >= 1000 {
		t.Errorf("esp=%.2fbps 超卫生带 |esp|<1000（红=顶替腿形态 175348bps）", esp)
	}
	// 对照：XAU 顶替 BTC 腿的垃圾形态（14:20 实拍 175348bps 同构）=int 除法标量，>1000bps。
	junk := (100050 - 4355) / 4355 * 10000
	if junk < 1000 {
		t.Errorf("垃圾形态对照应 >1000bps, got %d", junk)
	}
	_ = esp
}

// TestD419_OkxBtcPairRow D-419 S1 锚：monitorPairs() 含 okx:perp BTC 簿行
// {"okx:perp","BTC-USDT-SWAP","BTCUSDT-BTC","BTCUSDT"}——真钱组合#10 的 venue 簿
// （pair 键与 binance BTC 行同款、cex_venue 不同=三元组隔离本意）。
func TestD419_OkxBtcPairRow(t *testing.T) {
	want := monitorPair{"okx:perp", "BTC-USDT-SWAP", "BTCUSDT-BTC", "BTCUSDT"}
	found := false
	for _, p := range derivedPairs() {
		if p == want {
			found = true
		}
		// 三元组隔离：okx BTC 行不得复用 binance venue 标签
		if p.pair == "BTCUSDT-BTC" && p.cexVenue == "okx:perp" && p.sym != "BTC-USDT-SWAP" {
			t.Errorf("okx BTC 行 instId 应为 BTC-USDT-SWAP, got %s", p.sym)
		}
	}
	if !found {
		t.Fatalf("derivedPairs() 缺 okx BTC 簿行 %+v", want)
	}
}
