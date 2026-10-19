// backtest_test.go —— P3 锚（D-625）：①准入闸——BRENT 饥饿窗镜像必报「该窗
// 不可判」（票面验收锚）+健康窗放行+会话对齐缺；②回放数学（进出场/PnL 逐笔
// 精确）；③as-of-t 层（ts≤t 最新值，非时态 LatestFactsByKind 不可替代）。
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/platform/testutil/storetest"
)

// fakeSession 会话桩（known/openMin 可控）。
type fakeSession struct {
	known bool
	mins  int
}

func (f fakeSession) OpenMinutes(venue, symbol string, from, to time.Time) (int, bool) {
	return f.mins, f.known
}

func mkRow(base time.Time, minute int, entryNet, exitSpread float64, noise *float64) BookRow {
	return BookRow{
		TS:             base.Add(time.Duration(minute) * time.Minute),
		EntrySpreadBps: entryNet + 10, ExitSpreadBps: exitSpread,
		EntryNetBps: entryNet, FundingAnnualPct: 20,
		NoiseBps: noise,
	}
}

// TestAdmitStarvingBRENTWindow 票面验收锚镜像：BRENT 饥饿窗（3 行/24h+小时级断档）
// 必报「不可判」且理由具名（饥饿+断档）——禁产假 PnL 第一闸。
func TestAdmitStarvingBRENTWindow(t *testing.T) {
	base := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	rows := []BookRow{
		mkRow(base, 0, 80, 20, nil),
		mkRow(base, 200, 85, 25, nil),  // 断档 3h20m
		mkRow(base, 1400, 90, 30, nil), // 断档 20h
	}
	stats := WindowStatsFrom(rows, 5*time.Minute)
	ok, reasons := Admit(stats, fakeSession{known: false}, "3001157", "BRENT", 24*time.Hour, 5*time.Minute, 15*time.Minute)
	if ok {
		t.Fatalf("BRENT 饥饿窗必须不可判, reasons=%v", reasons)
	}
	joined := ""
	for _, r := range reasons {
		joined += r + ";"
	}
	if !strings.Contains(joined, "饥饿") || !strings.Contains(joined, "断档") {
		t.Fatalf("不可判理由须具名饥饿+断档: %v", reasons)
	}
	if stats.MaxGap != 20*time.Hour {
		t.Fatalf("MaxGap=20h 预期, got=%s", stats.MaxGap)
	}
}

// TestAdmitHealthyWindow 健康窗（5min 满拍 24h）放行；会话已知低对齐率→不可判。
func TestAdmitHealthyWindow(t *testing.T) {
	base := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	nz := 9.0
	var rows []BookRow
	for m := 0; m <= 24*60; m += 5 {
		rows = append(rows, mkRow(base, m, 80, 20, &nz))
	}
	stats := WindowStatsFrom(rows, 5*time.Minute)
	ok, reasons := Admit(stats, fakeSession{known: false}, "3001157", "BRENT", 24*time.Hour, 5*time.Minute, 15*time.Minute)
	if !ok {
		t.Fatalf("健康窗应放行, reasons=%v", reasons)
	}
	// 会话对齐：开市 300min 期望 60 行，实得 10 → 不可判。
	sparse := rows[:10]
	s2 := WindowStatsFrom(sparse, 5*time.Minute)
	ok2, reasons2 := Admit(s2, fakeSession{known: true, mins: 300}, "3001157", "BRENT", 24*time.Hour, 5*time.Minute, 15*time.Minute)
	if ok2 {
		t.Fatalf("对齐率 17%% 应不可判, reasons=%v", reasons2)
	}
	// 判别格：会话已知+满覆盖（1440min 全开=期望 288 行，实得 289）→必须放行。
	// （回归锚：期望行数算反方向时满覆盖窗也被误拒——会话品种全废。）
	s3 := WindowStatsFrom(rows, 5*time.Minute)
	ok3, reasons3 := Admit(s3, fakeSession{known: true, mins: 1440}, "3001157", "BRENT", 24*time.Hour, 5*time.Minute, 15*time.Minute)
	if !ok3 {
		t.Fatalf("会话已知满覆盖应放行, reasons=%v", reasons3)
	}
}

// TestReplayMath 回放状态机逐笔精确：entry_net≥阈进、exit_spread≤阈出、
// PnL=entry_net−exit_spread、持满 hold 强平、窗尾未平不记账。
func TestReplayMath(t *testing.T) {
	base := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	rows := []BookRow{
		mkRow(base, 0, 50, 30, nil),        // 低于 entry 阈 60 不进
		mkRow(base, 5, 80, 40, nil),        // 进（net 80）
		mkRow(base, 10, 85, 45, nil),       // exit 45>30 不出
		mkRow(base, 15, 70, 25, nil),       // 出（spread 25≤30）→ PnL=80−25=55
		mkRow(base, 20, 90, 10, nil),       // 进（net 90）
		mkRow(base, 24*60+30, 60, 15, nil), // hold 24h 满 → PnL=90−15=75
	}
	trades := Replay(rows, Params{EntryBps: 60, ExitBps: 30, HoldHours: 24})
	if len(trades) != 2 {
		t.Fatalf("2 笔预期, got=%d: %+v", len(trades), trades)
	}
	if trades[0].PnLBps != 55 || trades[1].PnLBps != 75 {
		t.Fatalf("PnL 55/75 预期, got %.0f/%.0f", trades[0].PnLBps, trades[1].PnLBps)
	}
	if trades[1].ExitTS.Sub(trades[1].EntryTS).Hours() < 24 {
		t.Fatalf("第二笔应持满强平")
	}
	mtx := Matrix(rows, []Params{
		{EntryBps: 60, ExitBps: 30, HoldHours: 24},
		{EntryBps: 200, ExitBps: 30, HoldHours: 24},
	})
	if len(mtx) != 2 || mtx[0].Trades != 2 || mtx[1].Trades != 0 {
		t.Fatalf("矩阵两格 2/0 笔预期, got=%+v", mtx)
	}
	if mtx[0].TotalBps != 130 || mtx[0].Wins != 2 {
		t.Fatalf("合计 130bps/2 胜预期, got=%+v", mtx[0])
	}
}

// TestLatestFactsByKindAsOf as-of-t 层 DB 锚：ts≤t 最新值（时点参数化）——
// 非时态 LatestFactsByKind 不可替代（回放拍 t 的「当时最新」唯一合法读口）。
func TestLatestFactsByKindAsOf(t *testing.T) {
	s := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `DELETE FROM facts WHERE kind='bt_asof_test'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.Pool().Exec(ctx, `DELETE FROM facts WHERE kind='bt_asof_test'`) })
	t1 := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	for _, f := range []struct {
		sym string
		v   float64
		ts  time.Time
	}{{"BTCUSDT", 100, t1}, {"BTCUSDT", 200, t2}, {"ETHUSDT", 50, t1}} {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ('bt_asof_test','binance',$1,$2,'x',$3,'bt')`,
			f.sym, f.v, f.ts); err != nil {
			t.Fatal(err)
		}
	}
	mid := t1.Add(30 * time.Minute)
	fs, err := s.LatestFactsByKindAsOf(ctx, "bt_asof_test", mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Fatalf("as-of 中点应 2 组（BTCUSDT/ETHUSDT）, got=%d", len(fs))
	}
	bySym := map[string]float64{}
	for _, f := range fs {
		bySym[f.Symbol] = f.Value
	}
	if bySym["BTCUSDT"] != 100 || bySym["ETHUSDT"] != 50 {
		t.Fatalf("中点应见 t1 值 100/50, got=%v", bySym)
	}
	fs2, err := s.LatestFactsByKindAsOf(ctx, "bt_asof_test", t2.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs2 {
		if f.Symbol == "BTCUSDT" && f.Value != 200 {
			t.Fatalf("t2 后应见 200, got=%v", f.Value)
		}
	}
	if _, _, _, ok, err := s.LatestTickAsOf(ctx, "nosuch", "NOSUCH", t2); err != nil || ok {
		t.Fatalf("零行应 ok=false 非 err: ok=%v err=%v", ok, err)
	}
}

// TestAdmitGapThresholdOverride 阈参数化锚（口径双修②）：同一 20min 断档——
// 15min 阈拒、30min 阈放行；Breaches 命中列表含起点+时长。
func TestAdmitGapThresholdOverride(t *testing.T) {
	base := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	nz := 9.0
	var rows []BookRow
	for m := 0; m <= 24*60; m += 5 {
		if m >= 100 && m < 120 {
			continue // 20min 断档
		}
		rows = append(rows, mkRow(base, m, 80, 20, &nz))
	}
	stats := WindowStatsFrom(rows, 5*time.Minute)
	if len(stats.Breaches) != 1 || stats.Breaches[0].Dur != 25*time.Minute {
		t.Fatalf("Breaches 应命中 25min 断档: %+v", stats.Breaches)
	}
	ok, reasons := Admit(stats, fakeSession{known: false}, "3001157", "BRENT", 24*time.Hour, 5*time.Minute, 15*time.Minute)
	if ok {
		t.Fatalf("25min 断档>15min 阈应拒: %v", reasons)
	}
	ok2, reasons2 := Admit(stats, fakeSession{known: false}, "3001157", "BRENT", 24*time.Hour, 5*time.Minute, 30*time.Minute)
	if !ok2 {
		t.Fatalf("30min 阈应放行: %v", reasons2)
	}
}

// TestMtTickSymMap 诊断键同域锚（口径双修①）：broker_symbol_map 三变体命中
// 返回实存 broker_symbol（ticks 存面键）；全 miss 回退裸 canon+注记如实缺。
func TestMtTickSymMap(t *testing.T) {
	s := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `DELETE FROM broker_symbol_map WHERE venue='bt_diag_v'; DELETE FROM broker_accounts WHERE venue='bt_diag_v'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(ctx, `DELETE FROM broker_symbol_map WHERE venue='bt_diag_v'; DELETE FROM broker_accounts WHERE venue='bt_diag_v'`)
	})
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES ('bt_diag_v','mt5','h',443,'p')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO broker_symbol_map (venue, broker_symbol, canonical) VALUES
		('bt_diag_v','BRENT.ft','BRENTUSD'),   -- canon 加 USD 变体命中
		('bt_diag_v','XAUUSD.p','XAUUSD')`); err != nil {
		t.Fatal(err)
	}
	sym, note := mtTickSym(ctx, s, "bt_diag_v", "BRENT")
	if sym != "BRENT.ft" || note != "" {
		t.Fatalf("BRENT 变体命中期 BRENT.ft: sym=%s note=%s", sym, note)
	}
	sym2, note2 := mtTickSym(ctx, s, "bt_diag_v", "NOSUCH")
	if sym2 != "NOSUCH" || note2 == "" {
		t.Fatalf("全 miss 应回退裸键+注记: sym=%s note=%s", sym2, note2)
	}
}
