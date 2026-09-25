// d376_paper_perp_test.go —— D-376 S4 B 面：perp×perp 纸面轨真库锚（先红后绿）。
// 六锚：开仓组合键/suspect 拒/反转两轮 normalized/age_7d/miss 两轮/accrual 数值。
package main

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
	"arb/internal/testutil/storetest"
)

// d376NormRow 构造 radar 内存行（funding nil=缺腿）。
func d376NormRow(venue, symbol, canon string, ann *float64) normRow {
	return normRow{Row: Row{Symbol: symbol, Vol24h: 1e7}, Venue: venue, Canonical: canon, FundingAnn: ann}
}

// d376OPRows 生产实证对（OP binance:okx，net=24.5）。
func d376OPRows() []normRow {
	hi, lo := 7.6, -16.9
	return []normRow{
		d376NormRow("binance", "OPUSDT", "OP", &hi),
		d376NormRow("okx", "OP-USDT-SWAP", "OP", &lo),
	}
}

func d376OPPair() divPair {
	return divPair{Canon: "OP", ShortVenue: "binance", LongVenue: "okx",
		ShortSym: "OPUSDT", LongSym: "OP-USDT-SWAP", FundShort: 7.6, FundLong: -16.9,
		NetAnn: 24.5, VolMin: 1e7}
}

// d376PerpStore 真库 store + 清 paper_spot_carry（行级隔离自持）。
func d376PerpStore(t *testing.T) *pgstore.Store {
	t.Helper()
	s := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	if _, err := s.Pool().Exec(context.Background(),
		`TRUNCATE paper_spot_carry, carry_graduations RESTART IDENTITY`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	return s
}

// 锚①：开纸面行 venue=a×b 组合键（方向在键）+structure=perp_perp+side/qty/entry 正确。
func TestD376_PerpOpen_ComboKey(t *testing.T) {
	s := d376PerpStore(t)
	ctx := context.Background()
	opened, exited, err := stepPaperPerpCarry(ctx, s, []divPair{d376OPPair()}, d376OPRows(), 20, time.Now())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 1 || opened[0] != "OP" || exited != 0 {
		t.Fatalf("锚①: opened=%v exited=%d, want [OP]/0", opened, exited)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	if len(active) != 1 {
		t.Fatalf("锚①: want 1 active, got %d", len(active))
	}
	p := active[0]
	if p.Venue != "binance×okx" || p.Symbol != "OP" || p.Side != "perp_perp" || p.Structure != "perp_perp" {
		t.Fatalf("锚①: venue/symbol/side/structure = %s/%s/%s/%s, want binance×okx/OP/perp_perp/perp_perp", p.Venue, p.Symbol, p.Side, p.Structure)
	}
	if math.Abs(p.EntryFundingAnn-24.5) > 1e-9 {
		t.Fatalf("锚①: entry_funding_ann = %v, want 24.5（net 已含方向）", p.EntryFundingAnn)
	}
	if p.MtVenue != nil {
		t.Fatalf("锚①: mt_venue want NULL, got %v", *p.MtVenue)
	}
}

// 锚②：suspect（|f|=500 单侧）不开仓、入 suspect 分流（splitDivPairs 列表）。
func TestD376_PerpSuspectNoOpen(t *testing.T) {
	s := d376PerpStore(t)
	ctx := context.Background()
	suspect := divPair{Canon: "PROVE", ShortVenue: "binance", LongVenue: "okx",
		ShortSym: "PROVEUSDT", LongSym: "PROVE-USDT", FundShort: -593, FundLong: -547, NetAnn: 46, VolMin: 1e7}
	rows := []normRow{
		d376NormRow("binance", "PROVEUSDT", "PROVE", fptr(-593.0)),
		d376NormRow("okx", "PROVE-USDT", "PROVE", fptr(-547.0)),
	}
	opened, _, err := stepPaperPerpCarry(ctx, s, []divPair{suspect}, rows, 20, time.Now())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("锚②: suspect 对开了仓: %v", opened)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	if len(active) != 0 {
		t.Fatalf("锚②: want 0 active, got %d", len(active))
	}
	// 分流锚：suspect 列表承接（digest 消费）
	_, _, suspectList := splitDivPairs([]divPair{suspect}, map[string]*bool{})
	if len(suspectList) != 1 || suspectList[0] != "PROVE" {
		t.Fatalf("锚②: suspectList = %v, want [PROVE]", suspectList)
	}
}

// 锚③（D-474a S3 改写）：funding 反转 signedNet 转负 → 当轮退 reason=adverse；
// 同键反向机会（okx 短/binance 长）放行重开。
func TestD376_PerpReversalExit(t *testing.T) {
	s := d376PerpStore(t)
	ctx := context.Background()
	pairs := []divPair{d376OPPair()}
	if _, _, err := stepPaperPerpCarry(ctx, s, pairs, d376OPRows(), 20, time.Now()); err != nil {
		t.Fatalf("open: %v", err)
	}
	// 反转：short 侧 −30 / long 侧 +10 → signedNet=−40 <0 → 当轮 adverse
	rev := []normRow{
		d376NormRow("binance", "OPUSDT", "OP", fptr(-30.0)),
		d376NormRow("okx", "OP-USDT-SWAP", "OP", fptr(10.0)),
	}
	if _, exited, err := stepPaperPerpCarry(ctx, s, pairs, rev, 20, time.Now()); err != nil || exited != 1 {
		t.Fatalf("锚③: 反转当轮 exited=%d err=%v, want 1（adverse）", exited, err)
	}
	board, _ := s.ListPaperSpotCarryBoard(ctx, 24*time.Hour)
	for _, p := range board {
		if p.Symbol == "OP" && p.Status == "exited" && p.ExitReason != "adverse" {
			t.Fatalf("锚③: exit_reason = %q, want adverse", p.ExitReason)
		}
	}
	// 反向对（okx 短 +10 / binance 长 −30 → signedNet=+40）同键可重开。
	revPair := divPair{Canon: "OP", ShortVenue: "okx", LongVenue: "binance",
		ShortSym: "OP-USDT-SWAP", LongSym: "OPUSDT", FundShort: 10.0, FundLong: -30.0,
		NetAnn: 40.0, VolMin: 1e7}
	opened, _, err := stepPaperPerpCarry(ctx, s, []divPair{revPair}, rev, 20, time.Now())
	if err != nil || len(opened) != 1 {
		t.Fatalf("锚③: 反向重开 opened=%v err=%v, want [OP]", opened, err)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	if len(active) != 1 || active[0].Venue != "okx×binance" {
		t.Fatalf("锚③: 反向行 want okx×binance active, got %+v", active)
	}
}

// 锚④：age≥7d → age_7d 退出（net 仍健康）。
func TestD376_PerpAgeExit(t *testing.T) {
	s := d376PerpStore(t)
	ctx := context.Background()
	pairs := []divPair{d376OPPair()}
	if _, _, err := stepPaperPerpCarry(ctx, s, pairs, d376OPRows(), 20, time.Now()); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `UPDATE paper_spot_carry SET entry_ts = now() - interval '8 days'`); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	_, exited, err := stepPaperPerpCarry(ctx, s, pairs, d376OPRows(), 20, time.Now())
	if err != nil || exited != 1 {
		t.Fatalf("锚④: exited=%d err=%v, want 1（age_7d）", exited, err)
	}
	board, _ := s.ListPaperSpotCarryBoard(ctx, 24*time.Hour)
	for _, p := range board {
		if p.Symbol == "OP" && p.Status == "exited" && p.ExitReason != "age_7d" {
			t.Fatalf("锚④: exit_reason = %q, want age_7d", p.ExitReason)
		}
	}
}

// 锚⑤（D-474a S4 改写）：任一侧 funding 行缺席 → miss_streak 独立计数（low_streak
// 不受污染），连缺 3 轮 → data_lost 退出（不混入 normalized）。
func TestD376_PerpMissExit(t *testing.T) {
	s := d376PerpStore(t)
	ctx := context.Background()
	pairs := []divPair{d376OPPair()}
	if _, _, err := stepPaperPerpCarry(ctx, s, pairs, d376OPRows(), 20, time.Now()); err != nil {
		t.Fatalf("open: %v", err)
	}
	half := d376OPRows()[:1] // okx 侧行缺席
	for i := 1; i <= 2; i++ {
		if _, exited, err := stepPaperPerpCarry(ctx, s, pairs, half, 20, time.Now()); err != nil || exited != 0 {
			t.Fatalf("锚⑤: miss 第 %d 轮 exited=%d err=%v, want 0", i, exited, err)
		}
		active, _ := s.ListActivePaperSpotCarry(ctx)
		if len(active) != 1 || active[0].MissStreak != i || active[0].LowStreak != 0 {
			t.Fatalf("锚⑤: miss 第 %d 轮 want miss=%d low=0, got %+v", i, i, active)
		}
	}
	if _, exited, err := stepPaperPerpCarry(ctx, s, pairs, half, 20, time.Now()); err != nil || exited != 1 {
		t.Fatalf("锚⑤: miss 第 3 轮 exited=%d err=%v, want 1（data_lost）", exited, err)
	}
	board, _ := s.ListPaperSpotCarryBoard(ctx, 24*time.Hour)
	for _, p := range board {
		if p.Symbol == "OP" && p.Status == "exited" && p.ExitReason != "data_lost" {
			t.Fatalf("锚⑤: exit_reason = %q, want data_lost", p.ExitReason)
		}
	}
}

// d376SeedSettle D-474a S1：种 funding_next_settle+funding_interval facts（8h UTC 网格），
// 无 funding_history → 各边界回落当期率（恒定率下与旧墙钟值同值：24h=3 界）。
func d376SeedSettle(t *testing.T, s *pgstore.Store, venue, symbol string, now time.Time) {
	t.Helper()
	iv := int64(8 * 3600)
	ns := time.Unix(now.Unix()/iv*iv+iv, 0)
	if err := s.InsertFacts(context.Background(), []fact.Fact{
		{Kind: fact.KindFundingNextSettle, Venue: venue, Symbol: symbol, Value: float64(ns.Unix()), Unit: fact.UnitUnixSec, Ts: now},
		{Kind: fact.KindFundingInterval, Venue: venue, Symbol: symbol, Value: float64(iv), Unit: fact.UnitSeconds, Ts: now},
	}); err != nil {
		t.Fatalf("seed settle %s|%s: %v", venue, symbol, err)
	}
}

// 锚⑥：accrual 数值——结算对齐后 24h 窗恰 3 界/腿，两腿各按当期率：
// short 腿 +7.6×3/1095×1000 + long 腿 +16.9×3/1095×1000 = 24.5×3/1095×10 ≈ 0.6712。
// （与旧墙钟公式同值——恒定率下 24h=3 期恒成立；断言保持 0.6712。）
func TestD376_PerpAccrual(t *testing.T) {
	s := d376PerpStore(t)
	ctx := context.Background()
	now := time.Now()
	d376SeedSettle(t, s, "binance", "OPUSDT", now)
	d376SeedSettle(t, s, "okx", "OP-USDT-SWAP", now)
	pairs := []divPair{d376OPPair()}
	if _, _, err := stepPaperPerpCarry(ctx, s, pairs, d376OPRows(), 20, now); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `UPDATE paper_spot_carry SET last_accrue_ts = now() - interval '24 hours'`); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if _, exited, err := stepPaperPerpCarry(ctx, s, pairs, d376OPRows(), 20, now); err != nil || exited != 0 {
		t.Fatalf("锚⑥: accrual 轮 exited=%d err=%v, want 0", exited, err)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	if len(active) != 1 {
		t.Fatalf("锚⑥: want 1 active, got %d", len(active))
	}
	want := 24.5 / 100 * active[0].QtyUSD * (3 * 8.0 / 8760.0)
	if math.Abs(active[0].AccruedUSD-want) > 1e-6 {
		t.Fatalf("锚⑥: accrued = %v, want %v", active[0].AccruedUSD, want)
	}
}
