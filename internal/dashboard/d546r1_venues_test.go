// d546r1_venues_test.go —— D-546 R1 扩面锚：okx 三面+gate 两面入三档树（归属正确+
// 立即可用优先活账本）/60s 缓存二次零拉/面败标缺不猜零。
package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/reconcile"
)

type fakeVenueAvail struct {
	calls int
	ret   *reconcile.VenueAvailability
}

func (f *fakeVenueAvail) Fetch(context.Context) (*reconcile.VenueAvailability, error) {
	f.calls++
	return f.ret, nil
}

// d546r1Fixture 夹具：okx/gate 活账本各一池+venue availability stub。
func d546r1Fixture(t *testing.T) (*Service, map[string]*fakeVenueAvail) {
	t.Helper()
	s := covTestStore(t)
	svc := covTestService(s)
	lg := bus.NewBalanceLedger()
	now := time.Now().UTC()
	lg.Put("okx:unified", 2080, 2100, "USDT", "ws", now)
	lg.Put("gate:perp", 0, 0, "USDT", "ws", now)
	svc.SetBalanceLedger(lg)
	fakes := map[string]*fakeVenueAvail{
		"okx": {ret: &reconcile.VenueAvailability{VenueKey: "okx:perp", Nodes: []*reconcile.AvailabilityNode{
			{Wallet: "trading", AvailableUSD: 2080, LockedUSD: 40, At: now},
			{Wallet: "funding", TransferableUSD: 12.5, At: now},
			{Wallet: "earn", TransferableUSD: 1235.16, At: now},
		}}},
		"gate": {ret: &reconcile.VenueAvailability{VenueKey: "gate:perp", Nodes: []*reconcile.AvailabilityNode{
			{Wallet: "spot", AvailableUSD: 500, LockedUSD: 30, At: now},
			{Wallet: "futures", AvailableUSD: 0, LockedUSD: 0, Detail: "total=0", At: now},
			{Wallet: "earn", Detail: "拉取失败: http 403 no earn permission", At: now},
		}}},
	}
	svc.SetVenueAvailFor(func(_ context.Context, venue string) (venueAvailFetcher, bool) {
		f, ok := fakes[venue]
		return f, ok
	})
	return svc, fakes
}

// 树完整性：okx 三面+gate 两面归档正确；活账本值压过 REST（立即可用同源 sizing）。
func TestD546R1_VenueTree(t *testing.T) {
	svc, _ := d546r1Fixture(t)
	resp := capMapResponse(context.Background(), t, svc)
	avail, transfer, locked := capTier(resp, "立即可用"), capTier(resp, "可转可用"), capTier(resp, "锁定/慢动")

	// 立即可用：okx=活账本 2080（非 REST trading.availEq 路径注记透出来源）。
	if n := capNodeByLabel(avail, "okx:unified available"); n == nil || n.AmountUsd != 2080 || !n.HasUsd {
		t.Fatalf("okx avail node=%+v", n)
	}
	if n := capNodeByLabel(avail, "gate:perp available"); n == nil {
		t.Fatal("gate:perp 节点缺")
	}
	// gate:spot 无私有流/快照→REST 面 500 兜底出节点。
	if n := capNodeByLabel(avail, "gate:spot available"); n == nil || n.AmountUsd != 500 {
		t.Fatalf("gate spot avail=%+v", n)
	}
	// 可转可用：okx funding+earn。
	if n := capNodeByLabel(transfer, "okx 资金钱包"); n == nil || n.AmountUsd != 12.5 {
		t.Fatalf("okx funding=%+v", n)
	}
	if n := capNodeByLabel(transfer, "okx 活期理财"); n == nil || n.AmountUsd != 1235.16 {
		t.Fatalf("okx earn=%+v", n)
	}
	// 锁定慢动：okx 占用/冻结 40、gate spot locked 30、gate earn 面败标缺。
	if n := capNodeByLabel(locked, "okx:unified 占用/冻结"); n == nil || n.AmountUsd != 40 {
		t.Fatalf("okx locked=%+v", n)
	}
	if n := capNodeByLabel(locked, "gate:spot locked"); n == nil || n.AmountUsd != 30 {
		t.Fatalf("gate spot locked=%+v", n)
	}
	if n := capNodeByLabel(locked, "gate 理财出借中"); n == nil || n.HasUsd {
		t.Fatalf("gate earn 应标缺: %+v", n)
	}
	if !resp.Degraded {
		t.Fatal("含标缺节点应 degraded")
	}
}

// 60s 缓存：连开两次 availability fetch 各 venue 恰 1 次。
func TestD546R1_AvailCacheTTL(t *testing.T) {
	svc, fakes := d546r1Fixture(t)
	ctx := context.Background()
	capMapResponse(ctx, t, svc)
	capMapResponse(ctx, t, svc)
	if fakes["okx"].calls != 1 || fakes["gate"].calls != 1 {
		t.Fatalf("calls okx=%d gate=%d 期望各 1", fakes["okx"].calls, fakes["gate"].calls)
	}
}

// venue 未配置→面标缺不猜零（其余面仍出）。
func TestD546R1_MissingVenue(t *testing.T) {
	svc, _ := d546r1Fixture(t)
	svc.SetVenueAvailFor(func(_ context.Context, venue string) (venueAvailFetcher, bool) {
		return nil, false
	})
	resp := capMapResponse(context.Background(), t, svc)
	avail := capTier(resp, "立即可用")
	// 活账本在→okx/gate avail 仍出（legPool 不依赖 availability 面）。
	if capNodeByLabel(avail, "okx:unified available") == nil {
		t.Fatal("活账本 okx 节点应仍出")
	}
	locked := capTier(resp, "锁定/慢动")
	for _, label := range []string{"okx:unified 占用/冻结", "gate:spot locked"} {
		if n := capNodeByLabel(locked, label); n == nil || n.HasUsd {
			t.Fatalf("%s 应标缺: %+v", label, n)
		}
	}
}

// R2 锚：REST 答了真 $0 须出诚实零节点（has_usd=true），非误标三径全空。
func TestD546R1_RestPresentZero(t *testing.T) {
	s := covTestStore(t)
	svc := covTestService(s)
	svc.SetVenueAvailFor(func(_ context.Context, venue string) (venueAvailFetcher, bool) {
		if venue != "gate" {
			return &fakeVenueAvail{ret: &reconcile.VenueAvailability{}}, true
		}
		return &fakeVenueAvail{ret: &reconcile.VenueAvailability{Nodes: []*reconcile.AvailabilityNode{
			{Wallet: "spot", AvailableUSD: 0, At: time.Now().UTC()}, // 真 $0、无 Detail
		}}}, true
	})
	resp := capMapResponse(context.Background(), t, svc)
	n := capNodeByLabel(capTier(resp, "立即可用"), "gate:spot available")
	if n == nil || !n.HasUsd || n.AmountUsd != 0 {
		t.Fatalf("REST 真$0 应出诚实零节点: %+v", n)
	}
	// 面败 Detail+全零 仍须标缺（对照锚）。
	if n := capNodeByLabel(capTier(resp, "锁定/慢动"), "gate:futures 保证金占用"); n == nil || n.HasUsd {
		t.Fatalf("面未出应标缺: %+v", n)
	}
}
