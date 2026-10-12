// D-493 S1/S5/S6：账户卡片报价健康透出 + 删除审计/凭据编辑 单元与集成测试。
package dashboard

import (
	"context"
	"fmt"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/platform/testutil/storetest"
	"arb/internal/store/pgstore"
	"arb/internal/trading/exec"

	v1 "arb/internal/gen/dashboardv1"
	"connectrpc.com/connect"
)

// TestD493_FeedStateOf 三态推导：live/stale/dark + 无样本 -1。
func TestD493_FeedStateOf(t *testing.T) {
	cases := []struct {
		age     time.Duration
		want    string
		wantAge float64
	}{
		{30 * time.Second, "live", 30},
		{6 * time.Minute, "stale", 360},
		{31 * time.Minute, "dark", 1860},
		{0, "dark", -1}, // 无样本
		{-time.Second, "dark", -1},
	}
	for _, c := range cases {
		st, age := feedStateOf(c.age)
		if st != c.want || age != c.wantAge {
			t.Fatalf("feedStateOf(%v) = %s,%.0f；want %s,%.0f", c.age, st, age, c.want, c.wantAge)
		}
	}
}

// TestD493_QuoteAgeByVenue qbus 按 Venue 取 max(Time)；零 Time 跳过；nil qbus 空表。
func TestD493_QuoteAgeByVenue(t *testing.T) {
	now := time.Now()
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "EURUSD", Venue: "1314615", Time: now.Add(-10 * time.Second)})
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "1314615", Time: now.Add(-2 * time.Second)}) // 更新者胜
	qb.Publish(bus.Quote{Symbol: "EURUSD", Venue: "2222222", Time: now.Add(-8 * time.Minute)})
	qb.Publish(bus.Quote{Symbol: "EURUSD", Venue: "3333333"}) // 零 Time → 跳过

	ages := quoteAgeByVenue(qb, now)
	if got := ages["1314615"]; got < time.Second || got > 5*time.Second {
		t.Fatalf("1314615 age=%v，want ~2s", got)
	}
	if got := ages["2222222"]; got < 7*time.Minute || got > 9*time.Minute {
		t.Fatalf("2222222 age=%v，want ~8m", got)
	}
	if _, ok := ages["3333333"]; ok {
		t.Fatalf("零 Time 报价不应入表")
	}
	if got := quoteAgeByVenue(nil, now); len(got) != 0 {
		t.Fatalf("nil qbus 应回空表，got %v", got)
	}
}

// TestD493_CredUpdateAndAudit DSN 集成：凭据编辑（空=不改）+ 删除审计事件 + 映射计数透出。
func TestD493_CredUpdateAndAudit(t *testing.T) {
	st := storetest.SharedStore(t, "ARB_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `DELETE FROM broker_accounts WHERE venue IN ('4930001','4930002')`); err != nil {
		t.Fatal(err)
	}
	seed := func(venue string) {
		if err := st.InsertBrokerAccount(ctx, &pgstore.BrokerAccount{
			Venue: venue, Platform: 1, Host: "h1", Port: 443, Password: "oldpw", Server: "s1", Status: "connected",
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed("4930001")
	seed("4930002")
	t.Cleanup(func() {
		st.Pool().Exec(ctx, `DELETE FROM broker_accounts WHERE venue IN ('4930001','4930002')`)
		st.Pool().Exec(ctx, `DELETE FROM execution_control_events WHERE scope_type='broker' AND scope_key='4930001'`)
	})

	// ① 凭据编辑：host+password 改动，port/server 留空不动。
	if err := st.UpdateBrokerCredentials(ctx, "4930001", "h2", 0, "", "newpw"); err != nil {
		t.Fatal(err)
	}
	a, err := st.GetBrokerAccount(ctx, "4930001")
	if err != nil {
		t.Fatal(err)
	}
	if a.Host != "h2" || a.Password != "newpw" || a.Port != 443 || a.Server != "s1" {
		t.Fatalf("凭据更新错误: %+v", a)
	}
	// 不存在的 venue → not found
	if err := st.UpdateBrokerCredentials(ctx, "9999998", "h", 0, "", ""); err == nil {
		t.Fatal("未存在 venue 应报 not found")
	}

	// ② 映射计数：插 2 条映射 → VenueSymbolMapCounts=2。
	for i, bs := range []string{"XAUUSD", "EURUSD"} {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, is_primary) VALUES ('4930001',$1,$2,true) ON CONFLICT DO NOTHING`, bs, fmt.Sprintf("C%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { st.Pool().Exec(ctx, `DELETE FROM broker_symbol_map WHERE venue='4930001'`) })
	counts, err := st.VenueSymbolMapCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["4930001"] != 2 {
		t.Fatalf("map count=%d，want 2", counts["4930001"])
	}

	// ③ 删除审计：scope_type='broker' actor='ui' reason 透传，删除后事件仍在。
	svc := &Service{store: st, bm: NewBrokerManager(st, bus.New(), exec.NewRegistry(), time.Minute)}
	if _, err := svc.RemoveBroker(ctx, connect.NewRequest(&v1.RemoveBrokerRequest{Venue: "4930001", Reason: "D-493 测试删除"})); err != nil {
		t.Fatal(err)
	}
	evs, err := st.ListExecutionControlEvents(ctx, "broker", "4930001")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 || evs[0].Reason != "D-493 测试删除" || evs[0].Actor != "ui" || evs[0].ToMode != "removed" || evs[0].FromMode != "connected" {
		t.Fatalf("审计事件错误: %+v", evs)
	}
	if _, err := st.GetBrokerAccount(ctx, "4930001"); err == nil {
		t.Fatal("账号应已删除")
	}
}
