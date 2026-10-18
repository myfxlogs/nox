// service_overview_bundle_orders_test.go —— 订单卡聚合+非空锚（热修反转条件漏网教训：
// 无锚=显示面改动绿灯假阴性——业主实拍「无在仓订单」抓到）。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"
)

// TestOverviewBundleOrdersNonEmptyAndAggregated 订单面三断言：
// ①库有单→resp.Orders 非空（反转条件回归锚）；②活期同型聚合=一卡（N 笔）；
// ③失败/已结单不入聚（那不是持仓）。
func TestOverviewBundleOrdersNonEmptyAndAggregated(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	svc := &Service{store: st}
	s := svc
	ctx := context.Background()
	seed := []pgstore.DCIOrder{
		{RequestID: "ovt1", Venue: "okx", TemplateID: "cash_mgmt", ProductRef: "okx-saving-USDT",
			Underlying: "USDT", OptType: "EARN", InvestCcy: "USDT", Qty: 1000, Status: "filled"},
		{RequestID: "ovt2", Venue: "okx", TemplateID: "earn_apr_lending", ProductRef: "okx-saving-USDT",
			Underlying: "USDT", OptType: "EARN", InvestCcy: "USDT", Qty: 500, Status: "filled"},
		{RequestID: "ovt3", Venue: "okx", TemplateID: "cash_mgmt", ProductRef: "okx-saving-USDT",
			Underlying: "USDT", OptType: "EARN", InvestCcy: "USDT", Qty: 300, Status: "failed"},
	}
	for i := range seed {
		if _, _, err := st.CreateDCIOrder(ctx, &seed[i]); err != nil {
			t.Fatal(err)
		}
	}
	// CreateDCIOrder 不落 status 列（默认 pending）——状态按场景直设后对拍。
	if _, err := st.Pool().Exec(ctx,
		`UPDATE fin_dci_order SET status='filled' WHERE request_id IN ('ovt1','ovt2')`); err != nil {
		t.Fatal(err)
	}
	cresp, err := s.GetOverviewBundle(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := cresp.Msg
	if len(resp.Orders) == 0 {
		db, _ := st.ListDCIOrders(ctx, 100)
		t.Fatalf("库有 filled 单但订单面空（反转条件回归）db=%d", len(db))
	}
	// ②聚合：okx 活期一张卡=1500（2 笔，failed 300 不入）
	found := false
	for _, o := range resp.Orders {
		if o.Kind == "earn" {
			found = true
			if o.AmountUsd != 1500 {
				t.Fatalf("聚合金额=%v want 1500", o.AmountUsd)
			}
			if o.Status != "LIVE" {
				t.Fatalf("聚合卡状态=%v want LIVE", o.Status)
			}
		}
	}
	if !found {
		t.Fatal("无聚合 earn 卡")
	}
	// ③失败单独立卡或不在 earn 聚合（300 不进聚合已由金额断言覆盖）
}

// TestOverviewBundleCritAlertDetails 总览三件修②锚（业主令 10-17——可见「是什么红」
// 非只计数）：24h 窗内 critical 告警→bundle 明细行=计数与明细同源同窗；
// 非 critical/窗外行不入明细。
func TestOverviewBundleCritAlertDetails(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	svc := &Service{store: st}
	s := svc
	ctx := context.Background()
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := st.Pool().Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO alerts (level, message, ts) VALUES
	      ('critical', '测试红一：kill 闸触发', now()),
	      ('critical', '测试红二：30d 硬线破', now()),
	      ('warn', '黄行不入明细', now()),
	      ('critical', '窗外 critical 不入', now() - interval '25 hours')`)
	cresp, err := s.GetOverviewBundle(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := cresp.Msg
	// 归组+封顶守恒（票面注记：原 len==count 断言随归组改写为
	// 「组数≤计数且 Σcount==计数」——截断尾行 count=被截告警合计故仍守恒）。
	if len(resp.CritAlerts) > int(resp.CritAlerts_24H) {
		t.Fatalf("组数 %d > 计数 %d", len(resp.CritAlerts), resp.CritAlerts_24H)
	}
	var sum int32
	for _, a := range resp.CritAlerts {
		sum += a.Count
	}
	if sum != resp.CritAlerts_24H {
		t.Fatalf("Σcount %d ≠ 计数 %d（守恒破）", sum, resp.CritAlerts_24H)
	}
	if len(resp.CritAlerts) < 2 {
		t.Fatalf("明细组=%d want ≥2（两测试红异文案=两组）", len(resp.CritAlerts))
	}
	msgs := map[string]bool{}
	for _, a := range resp.CritAlerts {
		msgs[a.Message] = true
		if a.Message == "" || a.Ts == nil {
			t.Fatalf("明细行缺文案/时戳: %+v", a)
		}
	}
	if !msgs["测试红一：kill 闸触发"] || !msgs["测试红二：30d 硬线破"] {
		t.Fatalf("文案原文未透传: %v", msgs)
	}
	if msgs["黄行不入明细"] || msgs["窗外 critical 不入"] {
		t.Fatalf("非 critical/窗外行漏入明细: %v", msgs)
	}
}

// TestOverviewBundleCritAlertGroupCap 归组+封顶锚（业主报障 10-18 同源霸屏票面）：
// ①N 条仅变数字段不同的同源告警→1 组 count=N+first/last 跨度；
// ②异文案→多组；③≥9 组→截断 ≤8+尾行「+N 组见告警页」且 Σcount 守恒。
func TestOverviewBundleCritAlertGroupCap(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	defer func() {
		st.Pool().Exec(ctx, `DELETE FROM alerts WHERE message LIKE '归组锚%' OR message LIKE '异组锚%'`)
	}()
	must := func(q string, args ...any) {
		if _, err := st.Pool().Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{store: st}
	// ①同源 30 条仅 age 数字段不同→1 组
	for i := 0; i < 30; i++ {
		must(`INSERT INTO alerts (level, message, ts) VALUES
			('critical', $1, now() - interval '1 minute' * $2)`,
			fmt.Sprintf("归组锚 fetch binance BTCUSDT: facts funding stale age=%dh%dm%ds", i, i%60, i%60), i)
	}
	// ②异文案两组
	must(`INSERT INTO alerts (level, message, ts) VALUES
		('critical', '异组锚A', now()), ('critical', '异组锚B', now())`)
	cresp, err := svc.GetOverviewBundle(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := cresp.Msg
	var g *v1.OverviewCritAlert
	for _, a := range resp.CritAlerts {
		if strings.HasPrefix(a.Message, "归组锚") {
			g = a
		}
	}
	if g == nil || g.Count != 30 {
		t.Fatalf("同源 30 条应塌为 1 组 count=30: %+v", g)
	}
	if g.FirstTs == nil || g.Ts == nil || !g.FirstTs.AsTime().Before(g.Ts.AsTime()) {
		t.Fatalf("first<last 跨度错: %+v", g)
	}
	var sum int32
	for _, a := range resp.CritAlerts {
		sum += a.Count
	}
	if sum != resp.CritAlerts_24H {
		t.Fatalf("Σcount %d ≠ %d（守恒破）", sum, resp.CritAlerts_24H)
	}
	// ③9 组异文案→截断 8+尾行
	must(`DELETE FROM alerts WHERE message LIKE '归组锚%' OR message LIKE '异组锚%'`)
	for i := 0; i < 9; i++ {
		must(`INSERT INTO alerts (level, message, ts) VALUES ('critical', $1, now())`,
			fmt.Sprintf("异组锚 distinct-%c", 'A'+rune(i)))
	}
	cresp, err = svc.GetOverviewBundle(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp = cresp.Msg
	if len(resp.CritAlerts) != critCardCap+1 {
		t.Fatalf("9 组应截为 %d 卡+尾行: %d", critCardCap, len(resp.CritAlerts))
	}
	tail := resp.CritAlerts[len(resp.CritAlerts)-1]
	if !strings.HasPrefix(tail.Message, "+") || !strings.Contains(tail.Message, "组见告警页") {
		t.Fatalf("尾行指路文案错: %q", tail.Message)
	}
	sum = 0
	for _, a := range resp.CritAlerts {
		sum += a.Count
	}
	if sum != resp.CritAlerts_24H {
		t.Fatalf("截断后 Σcount %d ≠ %d（守恒破）", sum, resp.CritAlerts_24H)
	}
}

// TestOverviewBundleIdleVsEarning 眠钱瓦片归组锚（STATE「眠钱口径修正+瓦片换面」
// 票·行220 范围②）：闲置组=Idle 旗池（income30d=0），在产组=有余额非闲池
// （earn/yield 入账面）——venue_type 先验分组退役。
func TestOverviewBundleIdleVsEarning(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	defer func() {
		st.Pool().Exec(ctx, `DELETE FROM fin_venue_account_snapshot WHERE venue IN ('idlev','mtidle');
			DELETE FROM income_events WHERE venue='idlev'; DELETE FROM broker_accounts WHERE venue='mtidle'`)
	}()
	now := time.Now()
	must := func(q string, args ...any) {
		if _, err := st.Pool().Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO fin_venue_account_snapshot
		(venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at) VALUES
		('idlev','earn',2000,2000,2000,'USDT',$1),('idlev','spot',500,500,500,'USDT',$1),
		('mtidle','mt5',5247,5247,5247,'USD',$1)`, now)
	must(`INSERT INTO broker_accounts (venue, venue_type, host, port, password, is_real)
		VALUES ('mtidle','mt5','h',443,'p',true) ON CONFLICT (venue) DO NOTHING`)
	// idlev:earn 池有 income→在产（venue_key 生成列=binance:earn 归属该池）
	must(`INSERT INTO income_events (venue, venue_type, symbol, income_type, amount_usd, tran_id, ts)
		VALUES ('idlev','earn','USDT','SAVINGS',1.5,900000001,$1)`, now)
	svc := &Service{store: st}
	cresp, err := svc.GetOverviewBundle(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	idle, earning := map[string]float64{}, map[string]float64{}
	for _, r := range cresp.Msg.Breakdowns {
		key := r.Label + ":" + r.Sub
		switch r.Group {
		case "idle":
			idle[key] = r.Amount
		case "earning":
			earning[key] = r.Amount
		}
	}
	// 在产组=idlev:earn（income 池）；闲置组=idlev:spot+mtidle:mt5（income=0）
	if earning["idlev:earn"] != 2000 {
		t.Fatalf("idlev:earn 应在产组: %+v", earning)
	}
	if idle["idlev:earn"] != 0 {
		t.Fatalf("idlev:earn 不应入闲置组: %+v", idle)
	}
	if idle["idlev:spot"] != 500 || idle["mtidle:mt5"] != 5247 {
		t.Fatalf("真闲池归组错: %+v", idle)
	}
}
