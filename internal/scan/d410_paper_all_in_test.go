// d410_paper_all_in_test.go —— D-410：纸面全账口径（S7 测试矩阵，先红后绿）。
// 红锚=498 形状（R0-B1 修正值）：accrued=+5、est_fee=6（正幅度）、收敛=+0.5 →
// netAllIn=−0.5 → 不毕业（改前 accrued>0 会毕业）。翻号过冲两结构各一。
package scan

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// d410Opts cfd 轨装配参数（缺省值同 wire：feeBps=5.0 与 cfg.XAssetCexFeeBps 缺省同源）。
func d410Opts() PaperCfdOpts {
	return PaperCfdOpts{AuthorizedMaxActive: 8, ReopenCooldown: 24 * time.Hour}
}

// d410Store DSN 库+本票涉及表清空（alerts/xasset_allow_combos/fin_xasset_monitor 不在
// d350 helper 清单内，本 helper 自持）。
func d410Store(t *testing.T) *pgstore.Store {
	t.Helper()
	s := d350Store(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE alerts, xasset_allow_combos RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate alerts/combos: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `DELETE FROM fin_xasset_monitor WHERE pair LIKE 'D410%'`); err != nil {
		t.Fatalf("clean monitor: %v", err)
	}
	return s
}

// d410SeedMonitor monitor 行（pair 带 D410 前缀隔离；noise_bps 非空使 LatestXAssetEntryGate ok）。
func d410SeedMonitor(t *testing.T, s *pgstore.Store, pair string, ts time.Time, entry float64) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(), `
		INSERT INTO fin_xasset_monitor (ts, pair, binance_bid, binance_ask, exness_bid, exness_ask,
			entry_spread_bps, exit_spread_bps, entry_net_bps, funding_rate_8h, funding_annual_pct,
			tracking_error_bps, equiv_annual_pct, noise_bps, cex_venue, mt_venue)
		VALUES ($1,$2,4000,4001,3999,4000,$3,5,3,0.001,21.9,1.0,24.9,9,'okx:perp','3001157:mt5')`, // D-423：venue 键=fixture 宇宙行 3001157×okx
		ts, pair, entry); err != nil {
		t.Fatalf("seed monitor %s: %v", pair, err)
	}
}

// TestD410_RedAnchor_498Shape A1 红锚：accrued=+5、est_fee=6（正幅度）、收敛=+0.5 →
// netAllIn=−0.5 → 不毕业（改前判据 accrued>0 会毕业——498 按期毕业=验「carry 在跑」非净赚钱）。
func TestD410_RedAnchor_498Shape(t *testing.T) {
	fee, conv := 6.0, 0.5
	p := gradPaper(1, "exited", 7, 1, 5.0, 0, "normalized") // accrued=+5>0（旧判据合格）
	p.EstRoundTripFeeUSD, p.ConvergenceUSD = &fee, &conv
	if graduationQualified(p, time.Now()) {
		t.Fatal("红锚：netAllIn=-0.5 应拒毕业（旧判据 accrued>0 会放行）")
	}
	// 对照：同一行收敛补足 → netAllIn=+1.0 → 合格
	conv2 := 2.0
	p.ConvergenceUSD = &conv2
	if !graduationQualified(p, time.Now()) {
		t.Fatal("对照组：netAllIn=+1.0 应毕业")
	}
}

// TestD410_ConvergenceFlip 翻号过冲两结构各一（R0-B2 钉死：带符号差×方向常数，
// 绝对值式在翻号域低估——cfd 例 entry=+12/now=−3 真捕获 +15bps vs 绝对值式 7bps）。
func TestD410_ConvergenceFlip(t *testing.T) {
	entry, last, qty := 10.0, -3.0, 1000.0
	// spot short_perp：basis=perp 溢价，dir=−1 → −1×(−3−10)=+13bps → +$1.3
	got := paperConvergenceUSD(paperBasisDir("short_perp", "spot_carry"), &entry, &last, qty)
	if got == nil || math.Abs(*got-1.3) > 1e-9 {
		t.Fatalf("spot short_perp 翻号：want +1.3, got %v", got)
	}
	// cfd long_cfd_short_perp：s_entry=+12/s_now=−3，dir=−1 → +15bps → +$1.5
	e2, l2 := 12.0, -3.0
	got = paperConvergenceUSD(paperBasisDir("short_perp", "long_cfd_short_perp"), &e2, &l2, qty)
	if got == nil || math.Abs(*got-1.5) > 1e-9 {
		t.Fatalf("cfd long_cfd_short_perp 翻号：want +1.5, got %v", got)
	}
	// cfd short_cfd_long_perp dir=+1：s_entry=−12/s_now=+3 → +15bps → +$1.5
	e3, l3 := -12.0, 3.0
	got = paperConvergenceUSD(paperBasisDir("long_perp", "short_cfd_long_perp"), &e3, &l3, qty)
	if got == nil || math.Abs(*got-1.5) > 1e-9 {
		t.Fatalf("cfd short_cfd_long_perp 翻号：want +1.5, got %v", got)
	}
	// basis 缺 → NULL 诚实缺（可算不可毕业）
	if paperConvergenceUSD(-1, &entry, nil, qty) != nil {
		t.Fatal("basis 缺应返 nil")
	}
}

// TestD410_SpotOpenFeeAndExitConvergence DSN 锚：spot 轨开仓记 est_fee（binance 覆盖集
// 内=qty×10bps/1e4=$1.0），退出冻结收敛=dir×(last−entry)×qty/1e4。
func TestD410_SpotOpenFeeAndExitConvergence(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	// uni 行带 basis=+10bps（开仓快照），funding=50（net≈49.85 在 band 内）
	f := 50.0
	vol := 100e6
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_cex_universe (venue, symbol, base, status, first_seen, tier, last_funding_ann, last_vol24h, last_basis_bps)
		VALUES ('binance','D410USDT','D410','TRADING',$1,'T3',$2,$3,10.0)`,
		time.Now().Add(-72*time.Hour), &f, &vol); err != nil {
		t.Fatalf("seed uni: %v", err)
	}
	es := eligibleStreak("binance|D410USDT")
	seedSettleFacts(t, s, "binance", "D410USDT", time.Now()) // D-474a S1：结算事实面
	opened, _, err := StepPaperSpotCarry(ctx, s, es, time.Now(), nil)
	if err != nil || len(opened) != 1 {
		t.Fatalf("open: %v opened=%v", err, opened)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	if len(active) != 1 {
		t.Fatalf("want 1 active, got %d", len(active))
	}
	p := active[0]
	// D-474a S2 分腿真费：binance spot 2×10bps + perp 2×4.5bps = 29bps × $1000 = $2.9
	if p.EstRoundTripFeeUSD == nil || math.Abs(*p.EstRoundTripFeeUSD-2.9) > 1e-9 {
		t.Fatalf("est_fee want 2.9（1000×29bps/1e4 分腿真费），got %v", p.EstRoundTripFeeUSD)
	}
	if p.EntryBasisBps == nil || *p.EntryBasisBps != 10.0 {
		t.Fatalf("entry_basis want 10, got %v", p.EntryBasisBps)
	}
	// 两轮低 funding（<10 → streak×2）触发 normalized 退出；basis 更新为 −3（翻号过冲）
	f2 := 5.0
	if _, err := s.Pool().Exec(ctx, `UPDATE fin_cex_universe SET last_funding_ann=$1, last_basis_bps=-3.0 WHERE symbol='D410USDT'`, &f2); err != nil {
		t.Fatalf("update uni: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := StepPaperSpotCarry(ctx, s, NewEntryStreak(), time.Now(), nil); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	var conv *float64
	var status string
	if err := s.Pool().QueryRow(ctx, `SELECT status, convergence_usd FROM paper_spot_carry WHERE id=$1`, p.ID).Scan(&status, &conv); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "exited" {
		t.Fatalf("want exited, got %s", status)
	}
	// short_perp dir=−1：−1×(−3−10)=+13bps → +$1.3
	if conv == nil || math.Abs(*conv-1.3) > 1e-9 {
		t.Fatalf("convergence want +1.3, got %v", conv)
	}
}

// TestD410_CfdMonitorBasis DSN 锚：cfd 开仓 entry_basis 接 monitor 面（ts<=entry 谓词），
// 每轮 last_basis 更 monitor 最新行，退出冻结收敛（翻号 +15bps）。
func TestD410_CfdMonitorBasis(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	pair := "D410CUSDT-D410C"
	d410SeedMonitor(t, s, pair, time.Now().Add(-time.Minute), 12.0) // entry=+12
	rows := []UniverseRow{d374Row("D410C", "3001157", "okx", "long_cfd_short_perp", 10.95, "")}
	opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil)
	if err != nil || len(opened) != 1 {
		t.Fatalf("open: %v opened=%v", err, opened)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	p := active[0]
	if p.EntryBasisBps == nil || *p.EntryBasisBps != 12.0 {
		t.Fatalf("entry_basis want 12（monitor 面），got %v", p.EntryBasisBps)
	}
	// D-474a S2 分腿真费：okx perp 2×5bps=$1.0 + MT 簿点差往返 2×1000×12bps/1e4=$2.4 → $3.4
	if p.EstRoundTripFeeUSD == nil || math.Abs(*p.EstRoundTripFeeUSD-3.4) > 1e-9 {
		t.Fatalf("est_fee want 3.4（okx 往返 1.0 + MT 簿实算 2.4），got %v", p.EstRoundTripFeeUSD)
	}
	// monitor 翻号到 −3 → 本轮 last_basis 更新
	d410SeedMonitor(t, s, pair, time.Now(), -3.0)
	if _, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil); err != nil {
		t.Fatalf("accrue: %v", err)
	}
	var last float64
	if err := s.Pool().QueryRow(ctx, `SELECT last_basis_bps FROM paper_spot_carry WHERE id=$1`, p.ID).Scan(&last); err != nil {
		t.Fatalf("read basis: %v", err)
	}
	if last != -3.0 {
		t.Fatalf("last_basis want -3, got %v", last)
	}
	// 宇宙行缺席三轮 → data_lost 退出（D-474a S4），冻结收敛=−1×(−3−12)=+15bps → +$1.5
	// D-500：真 miss=venue 有产出配对缺席——keepAlive 脏行保 venue 非静默。
	keepAlive := []UniverseRow{d374Row("KA410", "3001157", "okx", "long_cfd_short_perp", 0, "suspect")}
	for i := 0; i < 3; i++ {
		if _, exited, err := StepPaperCfdCarry(ctx, s, keepAlive, time.Now(), d410Opts(), nil); err != nil {
			t.Fatalf("miss step %d: %v", i, err)
		} else if i == 2 && exited != 1 {
			t.Fatalf("want exited=1, got %d", exited)
		}
	}
	var conv *float64
	if err := s.Pool().QueryRow(ctx, `SELECT convergence_usd FROM paper_spot_carry WHERE id=$1`, p.ID).Scan(&conv); err != nil {
		t.Fatalf("read conv: %v", err)
	}
	if conv == nil || math.Abs(*conv-1.5) > 1e-9 {
		t.Fatalf("convergence want +1.5, got %v", conv)
	}
}

// d410SeedCombo 白名单组合（enabled+simulated=false=real 域）。
func d410SeedCombo(t *testing.T, s *pgstore.Store, canonBuy, venueBuy, canonSell, venueSell string, simulated bool) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(), `
		INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell,
			max_long_qty, max_short_qty, max_gross_usd, enabled, simulated)
		VALUES ($1,$2,split_part($3,':',1),split_part($3,':',2),split_part($4,':',1),split_part($4,':',2),1,1,10000,true,$5)`,
		canonBuy, canonSell, venueBuy, venueSell, simulated); err != nil {
		t.Fatalf("seed combo: %v", err)
	}
}

// TestD410_GuaranteedSlot 保底段：授权组合非 top-N（net=2<8 地板下）仍开仓；
// 保底不占 PaperCfdMaxActive 槽（开票顺序钉死①）。
func TestD410_GuaranteedSlot(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	d410SeedCombo(t, s, "D410GUSD", "3001157:mt5", "D410GUSDT", "okx:perp", false)
	rows := []UniverseRow{
		d374Row("D410G", "3001157", "okx", "long_cfd_short_perp", 2.0, ""), // net<8 保底不套下限
	}
	// 6 个 top-N 合格行挤满 PaperCfdMaxActive——保底行仍须开出（不占 6 槽）
	for i, sym := range []string{"D410A", "D410B", "D410E", "D410F", "D410H", "D410I"} {
		rows = append(rows, d374Row(sym, "277842155", "binance", "long_cfd_short_perp", 20.0-float64(i), ""))
	}
	opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 7 {
		t.Fatalf("保底不占 top-N 槽：want opened=7（6 topN+1 保底），got %v", opened)
	}
	var va, vb string
	if err := s.Pool().QueryRow(ctx, `SELECT venue_a, venue_b FROM paper_spot_carry WHERE symbol='D410G'`).Scan(&va, &vb); err != nil {
		t.Fatalf("guaranteed row missing: %v", err)
	}
	if va != "3001157" || vb != "okx" {
		t.Fatalf("venue want 3001157×okx, got %s×%s", va, vb)
	}
}

// TestD410_GuaranteedFlap 钉死②flap 冷却：退出行 max(exit_ts)<24h 不重开；过窗重开。
func TestD410_GuaranteedFlap(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	d410SeedCombo(t, s, "D410FUSD", "3001157:mt5", "D410FUSDT", "okx:perp", false)
	rows := []UniverseRow{d374Row("D410F", "3001157", "okx", "long_cfd_short_perp", 2.0, "")}
	// 开 → 退（miss×3 → data_lost，D-474a S4 连缺阈值）→ 下轮保底在冷却内不得重开
	if _, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil); err != nil {
		t.Fatalf("open: %v", err)
	}
	keepAlive := []UniverseRow{d374Row("KA410F", "3001157", "okx", "long_cfd_short_perp", 0, "suspect")}
	for i := 0; i < 3; i++ {
		if _, _, err := StepPaperCfdCarry(ctx, s, keepAlive, time.Now(), d410Opts(), nil); err != nil {
			t.Fatalf("exit %d: %v", i, err)
		}
	}
	opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil)
	if err != nil {
		t.Fatalf("reopen step: %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("flap 冷却内不得重开，got opened=%v", opened)
	}
	// 把 exit_ts 拨回 25h 前 → 过窗重开
	if _, err := s.Pool().Exec(ctx, `UPDATE paper_spot_carry SET exit_ts=now()-interval '25 hours' WHERE symbol='D410F'`); err != nil {
		t.Fatal(err)
	}
	opened, _, err = StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil)
	if err != nil || len(opened) != 1 {
		t.Fatalf("过窗应重开：opened=%v err=%v", opened, err)
	}
}

// TestD410_GuaranteedDoubleCex 钉死③：双 CEX 组合跳过保底（无 MT 腿映射意义不硬塞）。
func TestD410_GuaranteedDoubleCex(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	d410SeedCombo(t, s, "D410DUSDT", "binance:perp", "D410DUSDT", "okx:perp", false)
	opened, _, err := StepPaperCfdCarry(ctx, s, nil, time.Now(), d410Opts(), nil)
	if err != nil || len(opened) != 0 {
		t.Fatalf("双 CEX 应跳过：opened=%v err=%v", opened, err)
	}
}

// TestD410_GuaranteedMissWarn 宇宙行缺席：不开仓+warn 节流（两轮连跑仍每轮一条；
// 禁 `_ =` 吞错——告警须真落库）。
func TestD410_GuaranteedMissWarn(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	d410SeedCombo(t, s, "ZZZUSD", "3001157:mt5", "ZZZUSDT", "okx:perp", false)
	// D-500：venue 零产出=静默不 warn——keepAlive 脏行保 venue 非静默后测「真缺席」warn。
	keepAlive := []UniverseRow{d374Row("KA410W", "3001157", "okx", "long_cfd_short_perp", 0, "suspect")}
	for i := 0; i < 2; i++ {
		if _, _, err := StepPaperCfdCarry(ctx, s, keepAlive, time.Now(), d410Opts(), nil); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE level='warn' AND message LIKE '%宇宙行缺席%'`).Scan(&n); err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if n != 2 {
		t.Fatalf("warn 节流每轮至多一条：2 轮 want 2, got %d", n)
	}
}

// TestD410_NullColumns 可空列 NULL 行扫描防回归（KB nullable-column 炸弹）+
// 「可算不可毕业」：est_fee NULL 行 net 照算但 graduationQualified 拒。
func TestD410_NullColumns(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	// venue 不在覆盖集（kraken）→ est_fee 拒填 NULL
	r := &pgstore.PaperSpotCarry{VenueA: "kraken", Symbol: "D410K", Side: "short_perp", EntryFundingAnn: 50, QtyUSD: 1000}
	if err := s.InsertPaperSpotCarry(ctx, r); err != nil {
		t.Fatalf("insert: %v", err)
	}
	rows, err := s.ListActivePaperSpotCarry(ctx)
	if err != nil {
		t.Fatalf("scan NULL 行: %v", err)
	}
	p := rows[0]
	if p.EstRoundTripFeeUSD != nil || p.ConvergenceUSD != nil || p.EntryBasisBps != nil {
		t.Fatal("NULL 列应扫描为 nil（防回归）")
	}
	// 可算：net=accrued+0−0 有值
	if netAllInUSD(5.0, p.EstRoundTripFeeUSD, p.ConvergenceUSD) != 5.0 {
		t.Fatal("缺项按 0 算展示口径")
	}
	// 不可毕业：est_fee NULL → graduationQualified 拒（KB validation-baseline-empty）
	p.Status, p.AccruedUSD = "exited", 5.0
	now := time.Now()
	p.EntryTs = now.Add(-7 * 24 * time.Hour)
	exitTs := now.Add(-time.Hour)
	p.ExitTs = &exitTs
	p.ExitReason = "normalized"
	if graduationQualified(p, now) {
		t.Fatal("est_fee NULL 行应拒毕业（可算不可毕业）")
	}
}

// TestD410_DemoLockedOut demo 行锁死回归：status=demo 永不毕业（隔离不变量）。
func TestD410_DemoLockedOut(t *testing.T) {
	fee, conv := 1.0, 1.0
	p := gradPaper(1, "demo", 30, 0, 100.0, 0, "")
	p.EstRoundTripFeeUSD, p.ConvergenceUSD = &fee, &conv
	if graduationQualified(p, time.Now()) {
		t.Fatal("demo 行永不毕业（锁死回归）")
	}
}
