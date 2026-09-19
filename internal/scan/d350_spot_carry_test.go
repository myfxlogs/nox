// d350_spot_carry_test.go —— D-350：spot_carry 第三结构（T1）+纸面轨三态（T2）。
// 红证：基线无此结构（scan.Store 接口无 universe 源=编译红）+持续性闸/累积公式变异红（见变异轮）。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// d350Store DSN 测试库（scan 包自建 helper：Migrate 幂等建全量 schema）。
func d350Store(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	s, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := s.Pool().Exec(context.Background(),
		`TRUNCATE fin_carry_universe, fin_cex_universe, paper_spot_carry RESTART IDENTITY`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	return s
}

// seedUni 插入 fin_cex_universe 一行。
func seedUni(t *testing.T, s *pgstore.Store, venue, symbol, status, tier string, firstSeenAgo time.Duration, funding float64) {
	t.Helper()
	f := funding
	if _, err := s.Pool().Exec(context.Background(), `
		INSERT INTO fin_cex_universe (venue, symbol, base, status, first_seen, tier, last_funding_ann)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		venue, symbol, symbol, status, time.Now().Add(-firstSeenAgo), tier, &f); err != nil {
		t.Fatalf("seed uni %s: %v", symbol, err)
	}
}

// TestD350_ScannerSpotCarryStructure T1：spot_carry 第三结构——TRADING+T1/T3+≥48h 入选，
// <48h 持续性闸与 T2 类排除；net=|funding|−0.15。
func TestD350_ScannerSpotCarryStructure(t *testing.T) {
	s := d350Store(t)
	seedUni(t, s, "binance", "GUSDT", "TRADING", "T3", 72*time.Hour, 91.9)
	seedUni(t, s, "binance", "B2USDT", "TRADING", "T3", 72*time.Hour, 120.4)
	seedUni(t, s, "binance", "HOMEUSDT", "TRADING", "T3", 1*time.Hour, 200)  // <48h 闸外
	seedUni(t, s, "binance", "AAPLUSDT", "TRADING", "T2", 72*time.Hour, 50)  // T2 类非加密跳过
	seedUni(t, s, "binance", "ZEROUSDT", "TRADING", "T3", 72*time.Hour, 0.5) // net<0 无意义仍落行（观测）

	res, err := NewCarryUniverseScanner(NewCarryUniverseStore(s)).Run(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	bySym := map[string]UniverseRow{}
	for _, r := range res.Rows {
		if r.Structure == "spot_carry" {
			bySym[r.Canonical] = r
		}
	}
	if len(bySym) != 3 {
		t.Fatalf("spot_carry rows = %d, want 3（HOME 闸外/AAPL T2 排除）: %+v", len(bySym), bySym)
	}
	g, ok := bySym["GUSDT"]
	if !ok {
		t.Fatal("GUSDT spot_carry 行缺失（红=现无此结构）")
	}
	if g.MtVenue != "(spot)" || g.CexVenue != "binance" {
		t.Errorf("GUSDT 行标记错: %+v", g)
	}
	if g.NetAnnual < 91.74 || g.NetAnnual > 91.76 {
		t.Errorf("GUSDT net = %v, want 91.9−0.15=91.75", g.NetAnnual)
	}
	if b, ok := bySym["B2USDT"]; !ok || b.NetAnnual < 120.24 || b.NetAnnual > 120.26 {
		t.Errorf("B2USDT 行缺失或 net 错: %+v", b)
	}
	if _, ok := bySym["HOMEUSDT"]; ok {
		t.Error("HOMEUSDT <48h 入选（持续性闸失效，红）")
	}
	if _, ok := bySym["AAPLUSDT"]; ok {
		t.Error("AAPLUSDT T2 类入选（应跳过）")
	}
}

// TestD350_PaperSpotCarryLifecycle T2：纸面轨三态数值锚——
// 开仓（net≥20 触发/side 按 funding 符号）、累积（92%/yr×1 天×$1000=$2.52；long_perp 收负 funding 为正）、
// 退出（|funding|<10 连 2 轮 normalized；满 7 天 age_7d）。
func TestD350_PaperSpotCarryLifecycle(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	seedUni(t, s, "binance", "GUSDT", "TRADING", "T3", 72*time.Hour, 92)
	seedUni(t, s, "binance", "NEGUSDT", "TRADING", "T3", 72*time.Hour, -30)

	// 开仓：两 symbol 各一行（side 按 funding 符号）
	opened, exited, err := StepPaperSpotCarry(ctx, s, now)
	if err != nil {
		t.Fatalf("step open: %v", err)
	}
	if opened != 2 || exited != 0 {
		t.Fatalf("opened=%d exited=%d, want 2/0（红=现无此轨）", opened, exited)
	}
	rows, err := s.ListActivePaperSpotCarry(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("active rows = %d: %v", len(rows), err)
	}
	for _, r := range rows {
		want := "short_perp"
		if r.Symbol == "NEGUSDT" {
			want = "long_perp"
		}
		if r.Side != want || r.QtyUSD != 1000 {
			t.Errorf("%s side/qty = %s/%v, want %s/1000", r.Symbol, r.Side, r.QtyUSD, want)
		}
	}
	// 累积：回拨 48h（2 天窗；任务书锚 92%/yr×1 天×$1000=$2.52 的 2 倍=5.0411，
	// 用 2 天是为让「去天数」变异可观测——1 天窗下公式退化等价）
	if _, err := s.Pool().Exec(ctx,
		`UPDATE paper_spot_carry SET last_accrue_ts = $1, entry_ts = $1`, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	opened, exited, err = StepPaperSpotCarry(ctx, s, now)
	if err != nil || opened != 0 || exited != 0 {
		t.Fatalf("step accrue: opened=%d exited=%d err=%v", opened, exited, err)
	}
	rows, _ = s.ListActivePaperSpotCarry(ctx)
	for _, r := range rows {
		want := 92.0 / 100 / 365 * 2 * 1000 // 5.0411（=任务书锚 $2.52/天 × 2 天）
		if r.Symbol == "NEGUSDT" {
			want = 30.0 / 100 / 365 * 2 * 1000 // long_perp 收负 funding=正收入
		}
		if diff := r.AccruedUSD - want; diff < -0.01 || diff > 0.01 {
			t.Errorf("%s accrued = %v, want ≈%.4f（红=累积公式去天数变形）", r.Symbol, r.AccruedUSD, want)
		}
	}
	// 退出 normalized：GUSDT funding 降 5 → 连 2 轮（预置 streak=1）
	if _, err := s.Pool().Exec(ctx,
		`UPDATE fin_cex_universe SET last_funding_ann = 5 WHERE symbol = 'GUSDT'`); err != nil {
		t.Fatalf("drop funding: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`UPDATE paper_spot_carry SET low_streak = 1, last_accrue_ts = $1 WHERE symbol = 'GUSDT'`, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("streak: %v", err)
	}
	_, exited, err = StepPaperSpotCarry(ctx, s, now)
	if err != nil {
		t.Fatalf("step exit: %v", err)
	}
	if exited != 1 {
		t.Fatalf("exited = %d, want 1（normalized）", exited)
	}
	var reason string
	if err := s.Pool().QueryRow(ctx,
		`SELECT exit_reason FROM paper_spot_carry WHERE symbol = 'GUSDT'`).Scan(&reason); err != nil {
		t.Fatalf("query exit: %v", err)
	}
	if reason != "normalized" {
		t.Errorf("exit_reason = %q, want normalized", reason)
	}
	// 退出 age_7d：NEGUSDT 满龄（funding 仍 −30 不在 normalized 线）
	if _, err := s.Pool().Exec(ctx,
		`UPDATE paper_spot_carry SET entry_ts = $1 WHERE symbol = 'NEGUSDT'`, now.Add(-8*24*time.Hour)); err != nil {
		t.Fatalf("age: %v", err)
	}
	_, exited, err = StepPaperSpotCarry(ctx, s, now)
	if err != nil || exited != 1 {
		t.Fatalf("step age exit: exited=%d err=%v", exited, err)
	}
	if err := s.Pool().QueryRow(ctx,
		`SELECT exit_reason FROM paper_spot_carry WHERE symbol = 'NEGUSDT'`).Scan(&reason); err != nil {
		t.Fatalf("query age exit: %v", err)
	}
	if reason != "age_7d" {
		t.Errorf("exit_reason = %q, want age_7d", reason)
	}
}
