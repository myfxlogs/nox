// service_allowrules_d445_test.go —— D-445 T1/T1b：venue 行实时资金列透传（Snapshot 主源，
// D-447：同源 poll 产物，测试先 Refresh 一次暖缓存）+流帧较新者规则双向锚。需 NOX_TEST_PG_DSN。
package dashboard

import (
	"context"
	"testing"
	"time"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/ledger/reconcile"
	"nox/internal/platform/testutil/storetest"

	"connectrpc.com/connect"
)

func d445Fixture(t *testing.T) (*Service, *AccountMonitor, *fakeAccountQuerier) {
	t.Helper()
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx,
		`DELETE FROM autotrade_allow_rules WHERE value ILIKE 'd445%'`); err != nil {
		t.Fatalf("clean: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated) VALUES
		('venue','d445x:mt5',true,false), ('canonical','D445X',true,false)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	q := &fakeAccountQuerier{key: "d445x:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "d445x:mt5", Venue: "d445x", VenueType: "mt5", AssetType: "cfd",
		Balance: 111.5, Equity: 222.25, Currency: "USD", Simulated: true,
	}}
	mon, _, _ := newAcctMonTest(t, q)
	mon.Refresh(ctx) // D-447：Snapshot 读 monitor cache——先跑一轮 poll 暖缓存
	return &Service{store: st, acctMon: mon}, mon, q
}

// T1：venue 行透传快照值+has_account；canonical 行 has_account=false。
func TestD445VenueMoneyColumns(t *testing.T) {
	svc, _, _ := d445Fixture(t)
	resp, err := svc.ListAutotradeAllowRules(context.Background(),
		connect.NewRequest(&v1.ListAutotradeAllowRulesRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var venue, canon *v1.AutotradeAllowRuleRow
	for _, r := range resp.Msg.Rules {
		switch r.Value {
		case "d445x:mt5":
			venue = r
		case "D445X":
			canon = r
		}
	}
	if venue == nil || canon == nil {
		t.Fatalf("rows missing: %+v", resp.Msg.Rules)
	}
	if !venue.HasAccount || venue.Balance != 111.5 || venue.Equity != 222.25 || venue.Currency != "USD" || !venue.Online || venue.AcctMode != "demo" {
		t.Fatalf("venue row=%+v", venue)
	}
	if canon.HasAccount || canon.AcctMode != "" {
		t.Fatalf("canonical has_account/acct_mode must be empty: %+v", canon)
	}
}

// T1b：较新者双向锚——流帧老（now−10m）→取快照值；流帧新（now+1s）→取流值。
func TestD445NewerSourceWins(t *testing.T) {
	svc, mon, q := d445Fixture(t)
	ctx := context.Background()

	// ① 流帧老 → 快照值胜（updated_at≈now）
	mon.ObserveStreamAccount("d445x:mt5", 999, 999, 0, 0, 0, 0, "EUR", time.Now().Add(-10*time.Minute))
	resp, err := svc.ListAutotradeAllowRules(ctx, connect.NewRequest(&v1.ListAutotradeAllowRulesRequest{}))
	if err != nil {
		t.Fatalf("list1: %v", err)
	}
	var row *v1.AutotradeAllowRuleRow
	for _, r := range resp.Msg.Rules {
		if r.Value == "d445x:mt5" {
			row = r
		}
	}
	if row == nil || row.Balance != 111.5 || row.Equity != 222.25 {
		t.Fatalf("snapshot should win: %+v", row)
	}

	// ② 流帧新（now+1s）→ 流值胜
	mon.ObserveStreamAccount("d445x:mt5", 999, 999, 0, 0, 0, 0, "EUR", time.Now().Add(time.Second))
	resp, err = svc.ListAutotradeAllowRules(ctx, connect.NewRequest(&v1.ListAutotradeAllowRulesRequest{}))
	if err != nil {
		t.Fatalf("list2: %v", err)
	}
	for _, r := range resp.Msg.Rules {
		if r.Value == "d445x:mt5" {
			row = r
		}
	}
	if row == nil || row.Balance != 999 || row.Currency != "EUR" || row.AcctMode != "demo" {
		t.Fatalf("stream should win (acct_mode falls back to snapshot): %+v", row)
	}

	// ③ querier 失败（快照空）→ 流帧兜底仍 has_account
	q.err = context.DeadlineExceeded
	mon2, _, _ := newAcctMonTest(t, q)
	mon2.Refresh(ctx) // 暖缓存：querier err → cache 空，断言流兜底路径不变
	mon2.ObserveStreamAccount("d445x:mt5", 777, 777, 0, 0, 0, 0, "USD", time.Now())
	svc2 := &Service{store: svc.store, acctMon: mon2}
	resp, err = svc2.ListAutotradeAllowRules(ctx, connect.NewRequest(&v1.ListAutotradeAllowRulesRequest{}))
	if err != nil {
		t.Fatalf("list3: %v", err)
	}
	row = nil
	for _, r := range resp.Msg.Rules {
		if r.Value == "d445x:mt5" {
			row = r
		}
	}
	if row == nil || !row.HasAccount || row.Balance != 777 || row.AcctMode != "" {
		t.Fatalf("stream fallback (acct_mode honest empty): %+v", row)
	}
}
