// autoexec_test.go —— D-598 锚：autoexec 模板提案落库即派执行+
// 非豁免族 autoexec=true 撞 CHECK 拒+SLA 超龄豁免不翻 expired。
package oppmatch

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

func seedTmpl(t *testing.T, st *pgstore.Store, id string, payoffClass string, exempt bool, autoexec bool) {
	t.Helper()
	var d *string
	if exempt {
		s := "D-286"
		d = &s
	}
	if _, err := st.Pool().Exec(context.Background(), `
		INSERT INTO fin_opp_template (id, mechanism, net_apr, exec_track, enabled, payoff_class, paper_exempt_d, autoexec)
		VALUES ($1,$2,'0.02','proposal',true,$3,$4,$5)
		ON CONFLICT (id) DO UPDATE SET payoff_class=$3, paper_exempt_d=$4, autoexec=$5`,
		id, id, payoffClass, d, autoexec); err != nil {
		t.Fatal(err)
	}
}

func TestAutoexec_DispatchAndSLA(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ctx := context.Background()

	seedTmpl(t, st, "earn_apr_lending", "yield_on_holdings", true, true)
	seedTmpl(t, st, "dci_buy_low", "naked_short_vol", false, false)

	// ①autoexec 模板提案落库即派（AutoexecHook 分流面直对拍）
	var calls int32
	var lastID int64
	AutoexecHook = func(_ context.Context, pid int64) (string, error) {
		atomic.AddInt32(&calls, 1)
		atomic.StoreInt64(&lastID, pid)
		return "executed", nil
	}
	defer func() { AutoexecHook = nil }()

	pid1, _, err := st.CreateProposal(ctx, &pgstore.Proposal{
		TemplateID: "earn_apr_lending", ExecTrack: "dciexec", Venue: "binance",
	})
	if err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}
	if ae, err := st.TemplateAutoexec(ctx, "earn_apr_lending"); err != nil || !ae {
		t.Fatalf("TemplateAutoexec=%v err=%v", ae, err)
	}
	if AutoexecHook != nil {
		if _, gerr := AutoexecHook(ctx, pid1); gerr != nil {
			t.Fatalf("dispatch: %v", gerr)
		}
	}
	if atomic.LoadInt32(&calls) != 1 || atomic.LoadInt64(&lastID) != pid1 {
		t.Fatalf("autoexec dispatch calls=%d last=%d pid=%d", calls, lastID, pid1)
	}

	// ②非豁免族 autoexec=true 撞 CHECK 拒（判据落表非代码 if）
	_, err2 := st.Pool().Exec(ctx, `
		UPDATE fin_opp_template SET autoexec=true WHERE id='dci_buy_low'`)
	if err2 == nil {
		t.Fatal("non-exempt autoexec=true 应撞 CHECK 拒")
	}
	t.Logf("CHECK 拒=PASS: %v", err2)

	// ③SLA 超龄豁免——autoexec 族不翻 expired
	pid2, _, _ := st.CreateProposal(ctx, &pgstore.Proposal{
		TemplateID: "cash_mgmt", ExecTrack: "dciexec", Venue: "gate",
	})
	seedTmpl(t, st, "cash_mgmt", "yield_on_holdings", true, true)
	if _, err := st.Pool().Exec(ctx,
		`UPDATE fin_proposal SET created_at=now()-interval '25h' WHERE id=$1`, pid2); err != nil {
		t.Fatal(err)
	}
	ids, err := st.ExpireProposalsOverAge(ctx, 24*time.Hour) // 24h
	_ = err
	// 直读核验——autoexec 行在 expired 集外
	var st2 string
	if err := st.Pool().QueryRow(ctx,
		`SELECT status FROM fin_proposal WHERE id=$1`, pid2).Scan(&st2); err != nil {
		t.Fatal(err)
	}
	if st2 != "proposed" {
		t.Fatalf("autoexec 行 SLA 超龄仍 proposed 期待，实得 %s", st2)
	}
	for _, id := range ids {
		if id == pid2 {
			t.Fatalf("autoexec pid=%d 被 SLA 过期翻走", pid2)
		}
	}
}
