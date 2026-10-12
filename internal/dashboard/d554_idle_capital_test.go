// d554_idle_capital_test.go —— D-554 锚：T1 判据纯函数零边界/T2 转换式状态机
// （只入闲置写 info+恢复写 recovered/连续闲置不刷/venue 未配置静默/fetch 败跳过）。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
)

// T1 判据纯函数：transferable>$500 且>avail×0.5→true；零边界全 false。
func TestD554_T1_IdleCapitalIdle(t *testing.T) {
	cases := []struct {
		name            string
		transfer, avail float64
		want            bool
	}{
		{"闲置典型", 1235.16, 80, true},
		{"低于下限", 499.99, 0, false},
		{"恰等下限", 500, 100, false},
		{"恰等比", 600, 1200, false}, // 600>600 不成立
		{"零avail", 600, 0, false}, // 无可用读数不误报
		{"负avail", 600, -1, false},
		{"不足比", 500.01, 2000, false},
	}
	for _, c := range cases {
		if got := IdleCapitalIdle(c.transfer, c.avail); got != c.want {
			t.Errorf("%s IdleCapitalIdle(%v,%v)=%v want %v", c.name, c.transfer, c.avail, got, c.want)
		}
	}
}

// T2 状态机：入闲置一条 info→连续不再写→恢复写 recovered→未配置/fetch 败静默。
func TestD554_T2_IdleCapitalMonitor(t *testing.T) {
	ctx := context.Background()
	f := &fakeGradDB{exchCfgs: []pgstore.ExchangeConfig{
		{ID: 1, Venue: "okx", VenueType: "spot"},
		{ID: 2, Venue: "okx", VenueType: "perp"}, // 去重
		{ID: 3, Venue: "binance", VenueType: "spot"},
		{ID: 4, Venue: "gate", VenueType: "perp", Simulated: true}, // sim 行不入
	}}
	svc := &Service{gradStore: f}
	okxFA := &fakeVenueAvail{ret: &reconcile.VenueAvailability{Nodes: []*reconcile.AvailabilityNode{
		{Wallet: "trading", AvailableUSD: 100, At: time.Now().UTC()},
		{Wallet: "funding", TransferableUSD: 800, At: time.Now().UTC()},
		{Wallet: "earn", Detail: "拉取失败: http 403", At: time.Now().UTC()}, // 面败不进合计
	}}}
	svc.SetVenueAvailFor(func(_ context.Context, venue string) (venueAvailFetcher, bool) {
		if venue == "okx" {
			return okxFA, true
		}
		return &fakeVenueAvail{}, true
	})
	mon := NewIdleCapitalMonitor(svc, time.Hour)

	mon.poll(ctx) // 入闲置：transfer=800>500 且>50 → 一条 info
	if f.alerts != 1 || len(f.alertMsgs) != 1 || !strings.Contains(f.alertMsgs[0], "闲置资金 okx") || !strings.Contains(f.alertMsgs[0], "$800.00") {
		t.Fatalf("入闲置告警错: n=%d msgs=%v", f.alerts, f.alertMsgs)
	}
	mon.poll(ctx) // 连续闲置：不再写
	if f.alerts != 1 {
		t.Fatalf("连续闲置应不刷: alerts=%d", f.alerts)
	}

	okxFA.ret = &reconcile.VenueAvailability{Nodes: []*reconcile.AvailabilityNode{
		{Wallet: "trading", AvailableUSD: 900, At: time.Now().UTC()},
	}}
	svc.capAvailCache, svc.capAvailAt = nil, nil // 越过 60s 缓存窗口换数据（生产=TTL 到期重拉）
	mon.poll(ctx)                                // 恢复：写 recovered
	if f.alerts != 2 || !strings.Contains(f.alertMsgs[1], "已恢复") {
		t.Fatalf("恢复告警错: n=%d msgs=%v", f.alerts, f.alertMsgs)
	}

	// venue 未配置→静默；fetch 败（va=nil）→跳过不告警。
	f.exchCfgs = nil
	f.alerts, f.alertMsgs = 0, nil
	mon.poll(ctx)
	if f.alerts != 0 {
		t.Fatalf("venue 未配置应静默: alerts=%d", f.alerts)
	}
	f.exchCfgs = []pgstore.ExchangeConfig{{ID: 9, Venue: "okx", VenueType: "spot"}}
	okxFA.ret = nil // fetcher 返回 nil VA
	svc.capAvailCache, svc.capAvailAt = nil, nil
	mon.poll(ctx)
	if f.alerts != 0 {
		t.Fatalf("fetch 败应跳过不告警: alerts=%d", f.alerts)
	}
}
