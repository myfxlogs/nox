// service_allowrules_d445_test.go —— D-445 T1/T1b：venue 行实时资金列透传（Refresh 主源）
// +流帧较新者规则双向锚。需 ARB_TEST_PG_DSN。
package dashboard

import (
	"context"
	"testing"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/reconcile"
	"arb/internal/testutil/storetest"

	"connectrpc.com/connect"
)

func d445Fixture(t *testing.T) (*Service, *AccountMonitor, *fakeAccountQuerier) {
	t.Helper()
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
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
		Balance: 111.5, Equity: 222.25, Currency: "USD",
	}}
	mon, _, _ := newAcctMonTest(t, q)
	return &Service{store: st, acctMon: mon}, mon, q
}

// T1：venue 行透传 Refresh 值+has_account；canonical 行 has_account=false。
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
	if !venue.HasAccount || venue.Balance != 111.5 || venue.Equity != 222.25 || venue.Currency != "USD" || !venue.Online {
		t.Fatalf("venue row=%+v", venue)
	}
	if canon.HasAccount {
		t.Fatalf("canonical has_account must be false: %+v", canon)
	}
}

// T1b：较新者双向锚——流帧老（now−10m）→取 Refresh 值；流帧新（now+1s）→取流值。
func TestD445NewerSourceWins(t *testing.T) {
	svc, mon, q := d445Fixture(t)
	ctx := context.Background()

	// ① 流帧老 → Refresh 值胜（updated_at≈now）
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
		t.Fatalf("refresh should win: %+v", row)
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
	if row == nil || row.Balance != 999 || row.Currency != "EUR" {
		t.Fatalf("stream should win: %+v", row)
	}

	// ③ querier 失败（Refresh miss）→ 流帧兜底仍 has_account
	q.err = context.DeadlineExceeded
	mon2, _, _ := newAcctMonTest(t, q)
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
	if row == nil || !row.HasAccount || row.Balance != 777 {
		t.Fatalf("stream fallback: %+v", row)
	}
}
