// d350_spot_carry_test.go —— D-350：spot_carry 第三结构（T1）+纸面轨三态（T2）。
// 红证：基线无此结构（scan.Store 接口无 universe 源=编译红）+持续性闸/累积公式变异红（见变异轮）。
package scan

import (
	"context"
	"fmt"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil/storetest"
)

// d350Store DSN 测试库（scan 包 helper：走 storetest.SharedStore 共享池，D-351 S1；
// 行级隔离由本 helper 的 TRUNCATE 自持）。
func d350Store(t *testing.T) *pgstore.Store {
	t.Helper()
	s := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	if _, err := s.Pool().Exec(context.Background(),
		`TRUNCATE fin_carry_universe, fin_cex_universe, paper_spot_carry RESTART IDENTITY`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	return s
}

// seedUni 插入 fin_cex_universe 一行（vol24hUSD<0 → NULL）。
func seedUni(t *testing.T, s *pgstore.Store, venue, symbol, status, tier string, firstSeenAgo time.Duration, funding float64) {
	t.Helper()
	seedUniVol(t, s, venue, symbol, status, tier, firstSeenAgo, funding, 100e6)
}

// seedUniVol 同 seedUni，显式 vol24h（D-350R1 v1.1 流动性闸锚用）。
func seedUniVol(t *testing.T, s *pgstore.Store, venue, symbol, status, tier string, firstSeenAgo time.Duration, funding, vol24h float64) {
	t.Helper()
	f := funding
	if _, err := s.Pool().Exec(context.Background(), `
		INSERT INTO fin_cex_universe (venue, symbol, base, status, first_seen, tier, last_funding_ann, last_vol24h)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		venue, symbol, symbol, status, time.Now().Add(-firstSeenAgo), tier, &f, &vol24h); err != nil {
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
	seedUni(t, s, "binance", "ZEROUSDT", "TRADING", "T3", 72*time.Hour, 0.5) // net<20 落表 floor 外（R1 S4 反转锚：不落行）

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
	if len(bySym) != 2 {
		t.Fatalf("spot_carry rows = %d, want 2（HOME 闸外/AAPL T2 排除/ZEROUSDT floor 外）: %+v", len(bySym), bySym)
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
	if _, ok := bySym["ZEROUSDT"]; ok {
		t.Error("ZEROUSDT net=0.35 落表（floor=net≥20 反转锚，R1 S4）")
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

	// 开仓：两 symbol 各一行（side 按 funding 符号；R1 S3 opened=symbol 清单按 net 降序）
	opened, exited, err := StepPaperSpotCarry(ctx, s, now)
	if err != nil {
		t.Fatalf("step open: %v", err)
	}
	if len(opened) != 2 || exited != 0 || opened[0] != "GUSDT" || opened[1] != "NEGUSDT" {
		t.Fatalf("opened=%v exited=%d, want [GUSDT NEGUSDT]/0（红=现无此轨）", opened, exited)
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
	if err != nil || len(opened) != 0 || exited != 0 {
		t.Fatalf("step accrue: opened=%v exited=%d err=%v", opened, exited, err)
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

// TestD350R1_TR4_GatesAndCap v1.1 闸校准：①vol24h<$10M 不开 ②net>300 band 外不开
// ③13 合格行 → active 恒 12（cap，候选按 net 降序）。
func TestD350R1_TR4_GatesAndCap(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	seedUniVol(t, s, "binance", "ILLIQUID", "TRADING", "T3", 72*time.Hour, 250, 5e6)  // vol<$10M（net=249.85 高位——若摘 vol 闸必入 cap 内，变异可观测）
	seedUniVol(t, s, "binance", "BANDOUT", "TRADING", "T3", 72*time.Hour, 350, 100e6) // band 外
	for i := 0; i < 13; i++ {
		seedUniVol(t, s, "binance", fmt.Sprintf("CAP%02dUSDT", i), "TRADING", "T3", 72*time.Hour, 150-float64(i), 50e6)
	}
	opened, exited, err := StepPaperSpotCarry(ctx, s, now)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	rows, _ := s.ListActivePaperSpotCarry(ctx)
	if len(rows) != 12 {
		t.Errorf("active = %d, want 12（cap，红=cap 摘除变形）", len(rows))
	}
	for _, r := range rows {
		if r.Symbol == "ILLIQUID" || r.Symbol == "BANDOUT" {
			t.Errorf("%s 不应开仓（vol/band 闸失效）", r.Symbol)
		}
	}
	if len(opened) != 12 {
		t.Errorf("opened = %d, want 12", len(opened))
	}
	// cap 降序：净收最高的 CAP00 在列、CAP12（最低）被 cap 挤出
	if _, err := s.Pool().Exec(ctx, `SELECT 1 WHERE false`); err != nil {
		t.Fatalf("noop: %v", err)
	}
	var hasLowest bool
	for _, r := range rows {
		if r.Symbol == "CAP12USDT" {
			hasLowest = true
		}
	}
	if hasLowest {
		t.Error("CAP12USDT（最低 net）不应在 cap 内")
	}
	_ = exited
}

// TestD350R1_TR5_DemoRowAccrues demo 行累积且不被自动退出（R1 S5）。
func TestD350R1_TR5_DemoRowAccrues(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	seedUni(t, s, "binance", "GUSDT", "TRADING", "T3", 72*time.Hour, 92)
	pr := &pgstore.PaperSpotCarry{Venue: "binance", Symbol: "GUSDT", Side: "short_perp",
		EntryFundingAnn: 92, QtyUSD: 1000, Note: "spot=x perp=y"}
	if err := s.InsertPaperSpotCarry(ctx, pr); err != nil {
		t.Fatalf("seed demo row: %v", err)
	}
	if err := s.MarkPaperSpotCarryDemo(ctx, pr.ID, pr.Note); err != nil {
		t.Fatalf("mark demo: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`UPDATE paper_spot_carry SET last_accrue_ts = $1`, now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	opened, exited, err := StepPaperSpotCarry(ctx, s, now)
	if err != nil || len(opened) != 0 || exited != 0 {
		t.Fatalf("step: opened=%v exited=%d err=%v", opened, exited, err)
	}
	accrable, err := s.ListAccrablePaperSpotCarry(ctx)
	if err != nil || len(accrable) != 1 {
		t.Fatalf("accrable = %d: %v", len(accrable), err)
	}
	r := accrable[0]
	if r.Status != "demo" {
		t.Errorf("status = %s, want demo（demo 行不被自动退出）", r.Status)
	}
	want := 92.0 / 100 / 365 * 1 * 1000 // 1 天
	if diff := r.AccruedUSD - want; diff < -0.01 || diff > 0.01 {
		t.Errorf("demo accrued = %v, want ≈%.4f（红=累积不含 demo 行）", r.AccruedUSD, want)
	}
}

// TestD350R2_NetDescUnderCap T-R2②：cap 场景排序键=net 降序——负 funding（long_perp）高 net 行
// 不被签名降序饿死（危害实证：ESPUSDT net 292.85 签名序垫底；age_7d 退出即重进使饿死可永久化）。
// 红=签名降序：+100 侧 12 行先填满 cap，NEG250USDT（net 249.85）恒排尾进不了 cap。
func TestD350R2_NetDescUnderCap(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	seedUniVol(t, s, "binance", "NEG250USDT", "TRADING", "T3", 72*time.Hour, -250, 100e6) // net=249.85 long_perp
	for i := 0; i < 12; i++ {
		seedUniVol(t, s, "binance", fmt.Sprintf("POS%02dUSDT", i), "TRADING", "T3", 72*time.Hour, 100, 100e6) // net=99.85 short_perp
	}
	opened, exited, err := StepPaperSpotCarry(ctx, s, now)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 12 || exited != 0 {
		t.Fatalf("opened=%d exited=%d, want 12/0", len(opened), exited)
	}
	if opened[0] != "NEG250USDT" {
		t.Errorf("opened[0] = %q, want NEG250USDT（net 249.85 降序榜首；红=签名降序负侧饿死）", opened[0])
	}
	rows, _ := s.ListActivePaperSpotCarry(ctx)
	var negSeen, negSide bool
	for _, r := range rows {
		if r.Symbol == "NEG250USDT" {
			negSeen, negSide = true, r.Side == "long_perp"
		}
	}
	if !negSeen {
		t.Fatal("NEG250USDT 未开仓（cap 下负侧饿死，红）")
	}
	if !negSide {
		t.Errorf("NEG250USDT side 非 long_perp（红=funding 符号判侧错）")
	}
}

// TestD350R1_TR2_PromotePaperToDemo 升级支路：active 行→promote→旧行封账+demo 行在册；
// 无 active 行（已 demo）→ ErrPaperPromoteNoActive。
func TestD350R1_TR2_PromotePaperToDemo(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	seedUni(t, s, "binance", "GUSDT", "TRADING", "T3", 72*time.Hour, 92)
	if _, _, err := StepPaperSpotCarry(ctx, s, time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("open paper: %v", err)
	}
	pr := &pgstore.PaperSpotCarry{Venue: "binance", Symbol: "GUSDT", Side: "short_perp",
		EntryFundingAnn: 90, QtyUSD: 500, Note: "spot=o1 perp=o2"}
	if err := s.PromotePaperToDemo(ctx, "binance", "GUSDT", pr); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if pr.ID == 0 || pr.Status != "demo" {
		t.Errorf("promote 返回行 = %+v, want demo 行 id>0", pr)
	}
	var status, reason string
	if err := s.Pool().QueryRow(ctx,
		`SELECT status, COALESCE(exit_reason,'') FROM paper_spot_carry ORDER BY id LIMIT 1`).Scan(&status, &reason); err != nil {
		t.Fatalf("query old: %v", err)
	}
	if status != "exited" || reason != "promoted_demo" {
		t.Errorf("旧行 = %s/%s, want exited/promoted_demo（纸面账封账保留）", status, reason)
	}
	var dstatus, note string
	if err := s.Pool().QueryRow(ctx,
		`SELECT status, note FROM paper_spot_carry ORDER BY id DESC LIMIT 1`).Scan(&dstatus, &note); err != nil {
		t.Fatalf("query new: %v", err)
	}
	if dstatus != "demo" || note != "spot=o1 perp=o2" {
		t.Errorf("新行 = %s/note=%q, want demo/双腿回执", dstatus, note)
	}
	// 无 active 行（已 demo）→ 明确错误
	err := s.PromotePaperToDemo(ctx, "binance", "GUSDT", &pgstore.PaperSpotCarry{Side: "short_perp", QtyUSD: 1})
	if err == nil || err != pgstore.ErrPaperPromoteNoActive {
		t.Errorf("重复 promote err = %v, want ErrPaperPromoteNoActive", err)
	}
}

// TestD354_PaperBasisColumns D-354 T2：开仓 entry_basis 落位（uni 有值/NULL 两态）
// +每轮 last_basis 随 uni 更新（entry 不变）。
func TestD354_PaperBasisColumns(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	seedUniVol(t, s, "binance", "BASGUSDT", "TRADING", "T3", 72*time.Hour, 92, 100e6)
	seedUniVol(t, s, "binance", "NULLBASUSDT", "TRADING", "T3", 72*time.Hour, 92, 100e6)
	if _, err := s.Pool().Exec(ctx,
		`UPDATE fin_cex_universe SET last_basis_bps = 50 WHERE symbol = 'BASGUSDT'`); err != nil {
		t.Fatalf("seed basis: %v", err)
	} // NULLBASUSDT 保持 NULL

	if _, _, err := StepPaperSpotCarry(ctx, s, now); err != nil {
		t.Fatalf("step open: %v", err)
	}
	rows, _ := s.ListActivePaperSpotCarry(ctx)
	got := map[string][2]*float64{} // [entry, last]
	for _, r := range rows {
		got[r.Symbol] = [2]*float64{r.EntryBasisBps, r.LastBasisBps}
	}
	b := got["BASGUSDT"]
	if b[0] == nil || *b[0] != 50 || b[1] == nil || *b[1] != 50 {
		t.Errorf("BASGUSDT entry/last basis = %v/%v, want 50/50（开仓时落 uni 当轮值）", b[0], b[1])
	}
	nb := got["NULLBASUSDT"]
	if nb[0] != nil || nb[1] != nil {
		t.Errorf("NULLBASUSDT 基差应 NULL 传播: %v/%v", nb[0], nb[1])
	}

	// 每轮更新：uni last_basis 50→30，重算后 last 变 30、entry 仍 50
	if _, err := s.Pool().Exec(ctx,
		`UPDATE fin_cex_universe SET last_basis_bps = 30 WHERE symbol = 'BASGUSDT'`); err != nil {
		t.Fatalf("upd basis: %v", err)
	}
	if _, _, err := StepPaperSpotCarry(ctx, s, now.Add(time.Hour)); err != nil {
		t.Fatalf("step 2: %v", err)
	}
	rows, _ = s.ListActivePaperSpotCarry(ctx)
	for _, r := range rows {
		if r.Symbol != "BASGUSDT" {
			continue
		}
		if r.LastBasisBps == nil || *r.LastBasisBps != 30 {
			t.Errorf("第二轮 last_basis = %v, want 30（红=每轮更新摘除）", r.LastBasisBps)
		}
		if r.EntryBasisBps == nil || *r.EntryBasisBps != 50 {
			t.Errorf("entry_basis 应保持开仓值 50, got %v", r.EntryBasisBps)
		}
	}
}
