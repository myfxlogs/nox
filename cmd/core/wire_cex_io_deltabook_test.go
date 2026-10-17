// wire_cex_io_deltabook_test.go —— D-602 差分落账核验收锚（退回㉓测试锚重写：
// 纯函数 deltaBookRows 直测生产径——错置 pgstore 包的手工重演测试已删）。
// 五断言：①注资残差→deposit 行含 status/ccy/amt ②窗内已有 io 行残差≈0 不落
// （双计回归锚，伤①）③PnL 抵扣不落 ④重跑幂等零新增（DB 级，InsertVenueIO
// ON CONFLICT+snap_id 稳定键）⑤PnL 查败跳窗（伤③ fail-closed）。
package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

// dtbSnaps 构造快照序列（ID 手工递增=幂等键稳定锚）。
func dtbSnaps() []pgstore.BalanceSnap {
	base := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	return []pgstore.BalanceSnap{
		{ID: 1, Balance: 1000, Ts: base},
		{ID: 2, Balance: 2000, Ts: base.Add(time.Hour)},     // 窗1：Δ=+1000 注资
		{ID: 3, Balance: 2100, Ts: base.Add(2 * time.Hour)}, // 窗2：Δ=+100（PnL 抵扣用例）
	}
}

func TestDeltaBookRows_DepositRowShape(t *testing.T) {
	rows, alerts, err := deltaBookRows(dtbSnaps(),
		func(from, to time.Time) (float64, error) {
			if from.Equal(dtbSnaps()[1].Ts) {
				return 100, nil // 窗2 PnL 解释全差
			}
			return 0, nil
		},
		func(from, to time.Time) (float64, error) { return 0, nil },
		"USD", 42, "110660304")
	if err != nil {
		t.Fatalf("deltabook: %v", err)
	}
	if len(rows) != 1 || len(alerts) != 1 {
		t.Fatalf("注资残差须落 1 行 1 alert: rows=%d alerts=%d", len(rows), len(alerts))
	}
	r := rows[0]
	if r.Direction != "deposit" || r.Status != "inferred" || r.Ccy != "USD" || r.Amount != 1000 {
		t.Errorf("① 落账行形态: %+v, want deposit/inferred/USD/1000", r)
	}
	if r.ExternalID != "mt5:delta:2" {
		t.Errorf("幂等键须锚 cur snap_id（小项⑸）: %q", r.ExternalID)
	}
	if r.ConfigID != 42 || r.Venue != "110660304" {
		t.Errorf("归属列: %+v", r)
	}
}

func TestDeltaBookRows_IOExplainedSkipped(t *testing.T) {
	// 伤①双计回归锚：窗1 注资 1000 已由 order 面落 io 行（SumVenueIOExternal=1000）
	// →残差≈0 不落（ICR/EXR 户同型不再双计）。
	calls := 0
	rows, _, err := deltaBookRows(dtbSnaps(),
		func(from, to time.Time) (float64, error) { return 0, nil },
		func(from, to time.Time) (float64, error) {
			calls++
			if from.Equal(dtbSnaps()[1].Ts) {
				return 100, nil // 窗2 io 解释全差
			}
			return 1000, nil // 窗1 已落 io 行（order 面先行）解释注资
		},
		"USD", 42, "110660304")
	if err != nil {
		t.Fatalf("deltabook: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("② 窗内已落 io 行解释全差须不落（双计回归）: got %d rows", len(rows))
	}
	if calls < 2 {
		t.Errorf("每窗都须扣 ioSum: calls=%d", calls)
	}
}

func TestDeltaBookRows_PnLExplainedSkipped(t *testing.T) {
	// ③ PnL 抵扣：窗2 Δ=+100 全由关仓 PnL 解释 → 不落。
	rows, _, err := deltaBookRows(dtbSnaps(),
		func(from, to time.Time) (float64, error) {
			if from.Equal(dtbSnaps()[1].Ts) {
				return 100, nil // 窗2 PnL=+100 抵扣全差
			}
			return 0, nil
		},
		func(from, to time.Time) (float64, error) {
			if from.Equal(dtbSnaps()[0].Ts) {
				return 1000, nil // 窗1 已落 io 解释注资
			}
			return 0, nil
		},
		"USD", 42, "110660304")
	if err != nil {
		t.Fatalf("deltabook: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("③ PnL 抵扣残差≈0 须不落: got %d rows", len(rows))
	}
}

func TestDeltaBookRows_PnLQueryFailClosed(t *testing.T) {
	// ⑤ 伤③：PnL 查败=整户 err（查败 pnl=0 会把仓平盈亏记成入金——本票病灶自体）。
	rows, alerts, err := deltaBookRows(dtbSnaps(),
		func(from, to time.Time) (float64, error) { return 0, errors.New("db down") },
		func(from, to time.Time) (float64, error) { return 0, nil },
		"USD", 42, "110660304")
	if err == nil {
		t.Fatal("⑤ PnL 查败须 fail-closed 返错")
	}
	if len(rows) != 0 || len(alerts) != 0 {
		t.Errorf("查败不得产出行/alert: %v %v", rows, alerts)
	}
}

func TestDeltaBookRows_IDEMPOTENTViaDB(t *testing.T) {
	// ④ 重跑幂等零新增（DB 级：同窗重跑同 snap_id 键→ON CONFLICT DO NOTHING）。
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM fin_venue_io WHERE external_id LIKE 'mt5:delta:%' OR venue='110660304' AND external_id LIKE 'mt5:%'`,
		`DELETE FROM fin_venue_account_snapshot WHERE venue='110660304' AND src='test'`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM fin_venue_io WHERE external_id LIKE 'mt5:delta:%'`,
			`DELETE FROM fin_venue_account_snapshot WHERE venue='110660304' AND src='test'`,
		} {
			_, _ = st.Pool().Exec(context.Background(), q)
		}
	})
	// seed 两帧 src=mt_event 事件行（D-604 输入门内）。
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO fin_venue_account_snapshot (venue, venue_type, balance_raw, currency, fetched_at, src)
		VALUES ('110660304','mt5',1000,'USD',now()-interval '3 hours','mt_event'),
		       ('110660304','mt5',2000,'USD',now()-interval '2 hours','mt_event')`); err != nil {
		t.Fatalf("seed snaps: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`DELETE FROM fin_venue_account_snapshot WHERE venue='110660304' AND src IN ('mt_frame','mt_event','test')`); err != nil {
		t.Fatalf("pre-clean: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO fin_venue_account_snapshot (venue, venue_type, balance_raw, currency, fetched_at, src)
		VALUES ('110660304','mt5',1000,'USD',now()-interval '3 hours','mt_event'),
		       ('110660304','mt5',2000,'USD',now()-interval '2 hours','mt_event')`); err != nil {
		t.Fatalf("seed snaps: %v", err)
	}
	snaps, err := st.BalanceDeltaWindows(ctx, "110660304", time.Now().Add(-24*time.Hour))
	if err != nil || len(snaps) != 2 {
		t.Fatalf("snaps=%d err=%v（src 门内事件行须入）", len(snaps), err)
	}
	rows, _, err := deltaBookRows(snaps,
		func(from, to time.Time) (float64, error) { return 0, nil },
		func(from, to time.Time) (float64, error) { return 0, nil },
		"USD", 42, "110660304")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if n, err := st.InsertVenueIO(ctx, rows); err != nil || n != 1 {
		t.Fatalf("first insert: n=%d err=%v", n, err)
	}
	// 重跑：同输入同键 → 零新增。
	rows2, _, err := deltaBookRows(snaps,
		func(from, to time.Time) (float64, error) { return 0, nil },
		func(from, to time.Time) (float64, error) { return 0, nil },
		"USD", 42, "110660304")
	if err != nil {
		t.Fatalf("rerun rows: %v", err)
	}
	if n, err := st.InsertVenueIO(ctx, rows2); err != nil || n != 0 {
		t.Errorf("④ 重跑幂等零新增: n=%d err=%v", n, err)
	}
}
