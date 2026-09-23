// d376_paper_perp_test.go —— D-376 S4 B 面：perp×perp 纸面轨真库锚（先红后绿）。
// 六锚：开仓组合键/suspect 拒/反转两轮 normalized/age_7d/miss 两轮/accrual 数值。
package main

import (
	"context"
	"math"
	"testing"
	"time"

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

// 锚③：funding 反转 signedNet 转负两轮 → normalized（方向入场固定，重算可反噬）。
func TestD376_PerpReversalExit(t *testing.T) {
	s := d376PerpStore(t)
	ctx := context.Background()
	pairs := []divPair{d376OPPair()}
	if _, _, err := stepPaperPerpCarry(ctx, s, pairs, d376OPRows(), 20, time.Now()); err != nil {
		t.Fatalf("open: %v", err)
	}
	// 反转：short 侧 −30 / long 侧 +10 → signedNet=−40 < divPct
	rev := []normRow{
		d376NormRow("binance", "OPUSDT", "OP", fptr(-30.0)),
		d376NormRow("okx", "OP-USDT-SWAP", "OP", fptr(10.0)),
	}
	if _, exited, err := stepPaperPerpCarry(ctx, s, pairs, rev, 20, time.Now()); err != nil || exited != 0 {
		t.Fatalf("锚③: 反转第 1 轮 exited=%d err=%v, want 0（streak=1）", exited, err)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	if len(active) != 1 || active[0].LowStreak != 1 {
		t.Fatalf("锚③: 反转第 1 轮 want streak=1, got %+v", active)
	}
	if _, exited, err := stepPaperPerpCarry(ctx, s, pairs, rev, 20, time.Now()); err != nil || exited != 1 {
		t.Fatalf("锚③: 反转第 2 轮 exited=%d err=%v, want 1（normalized）", exited, err)
	}
	board, _ := s.ListPaperSpotCarryBoard(ctx, 24*time.Hour)
	for _, p := range board {
		if p.Symbol == "OP" && p.Status == "exited" && p.ExitReason != "normalized" {
			t.Fatalf("锚③: exit_reason = %q, want normalized", p.ExitReason)
		}
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

// 锚⑤：任一侧 funding 行缺席两轮 → normalized（行在=信号在，数据死宁可早退）。
func TestD376_PerpMissExit(t *testing.T) {
	s := d376PerpStore(t)
	ctx := context.Background()
	pairs := []divPair{d376OPPair()}
	if _, _, err := stepPaperPerpCarry(ctx, s, pairs, d376OPRows(), 20, time.Now()); err != nil {
		t.Fatalf("open: %v", err)
	}
	half := d376OPRows()[:1] // okx 侧行缺席
	if _, exited, err := stepPaperPerpCarry(ctx, s, pairs, half, 20, time.Now()); err != nil || exited != 0 {
		t.Fatalf("锚⑤: miss 第 1 轮 exited=%d err=%v, want 0", exited, err)
	}
	if _, exited, err := stepPaperPerpCarry(ctx, s, pairs, half, 20, time.Now()); err != nil || exited != 1 {
		t.Fatalf("锚⑤: miss 第 2 轮 exited=%d err=%v, want 1（normalized）", exited, err)
	}
}

// 锚⑥：accrual 数值——signedNet/100/365×days×qty：net=24.5、days=1、qty=1000 → 0.6712。
// （SSOT 锚文「0.0671」为笔误：24.5/100/365×1000=0.6712，公式按 §2B 逐字实现，此断言为公式真值。）
func TestD376_PerpAccrual(t *testing.T) {
	s := d376PerpStore(t)
	ctx := context.Background()
	pairs := []divPair{d376OPPair()}
	if _, _, err := stepPaperPerpCarry(ctx, s, pairs, d376OPRows(), 20, time.Now()); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `UPDATE paper_spot_carry SET last_accrue_ts = now() - interval '24 hours'`); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if _, exited, err := stepPaperPerpCarry(ctx, s, pairs, d376OPRows(), 20, time.Now()); err != nil || exited != 0 {
		t.Fatalf("锚⑥: accrual 轮 exited=%d err=%v, want 0", exited, err)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	if len(active) != 1 {
		t.Fatalf("锚⑥: want 1 active, got %d", len(active))
	}
	want := 24.5 / 100 / 365 * 1.0 * active[0].QtyUSD
	if math.Abs(active[0].AccruedUSD-want) > 1e-6 {
		t.Fatalf("锚⑥: accrued = %v, want %v", active[0].AccruedUSD, want)
	}
}
