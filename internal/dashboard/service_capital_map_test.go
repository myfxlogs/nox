// service_capital_map_test.go —— D-546 S2/S3 验收锚：
// A2 树完整性（六面全出+理财活期在「可转」不在「立即可用」+折 USD 注记）/
// A3 sizing 基准不变（全景采集器零进 sizing 路径）/ A4 二开零 REST（60s 缓存计数）/
// 降级语义（实拉失败/缺价未折算=degraded 如实标缺不猜）。
package dashboard

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
	"connectrpc.com/connect"
)

// fakeCapGaps 计数 fake（A4 计数断言用）。
type fakeCapGaps struct {
	calls int
	ret   *reconcile.CapitalGaps
}

func (f *fakeCapGaps) Fetch(ctx context.Context) (*reconcile.CapitalGaps, error) {
	f.calls++
	return f.ret, nil
}

// capMapFixture 真库夹具：快照三池（funding 行）+活账本两池（spot locked 推导源）+计数 fake。
func capMapFixture(t *testing.T) (*Service, *fakeCapGaps) {
	t.Helper()
	s := covTestStore(t)
	now := time.Now().UTC()
	p5, f5 := 5.0, 5.0
	svc := covTestService(s)
	// #1c：账户面=acctMon 实时态（快照表种子退役——funding 池行唯一来源即实时视图）。
	seedLiveAccounts(t, svc, []pgstore.VenueAccountLatest{
		{Venue: "binance", VenueType: "funding", BalanceRaw: &p5, EquityRaw: &f5, FreeMarginRaw: &p5, Currency: "USDT", FetchedAt: now},
	})
	lg := bus.NewBalanceLedger()
	lg.Put("binance:spot", 100, 110, "USDT", "ws", now) // locked=110−100=10
	lg.Put("binance:perp", 50, 60, "USDT", "ws", now)
	svc.SetBalanceLedger(lg)
	fake := &fakeCapGaps{ret: &reconcile.CapitalGaps{
		EarnFlexibleUSDT: 30, EarnLockedUSDT: 7, EarnTotalUSDT: 37,
		CoinM:     []reconcile.CoinMBalance{{Asset: "BTC", Balance: 0.05, Available: 0.04}},
		Options:   []reconcile.OptionsBalance{{Asset: "USDT", Equity: 617.77, Available: 300}},
		FetchedAt: now,
	}}
	svc.SetCapitalGapFetcher(fake)
	// D-546 R1：okx/gate 面 stub（值非零防标缺扰 degraded 锚；树归属断言见 d546r1）。
	svc.SetVenueAvailFor(func(_ context.Context, venue string) (venueAvailFetcher, bool) {
		var va *reconcile.VenueAvailability
		switch venue {
		case "okx":
			va = &reconcile.VenueAvailability{Nodes: []*reconcile.AvailabilityNode{
				{Wallet: "trading", AvailableUSD: 100, LockedUSD: 5, At: now},
				{Wallet: "funding", TransferableUSD: 1, At: now},
				{Wallet: "earn", TransferableUSD: 2, At: now},
			}}
		default:
			va = &reconcile.VenueAvailability{Nodes: []*reconcile.AvailabilityNode{
				{Wallet: "spot", AvailableUSD: 10, LockedUSD: 1, At: now},
				{Wallet: "futures", AvailableUSD: 5, At: now},
				{Wallet: "earn", LockedUSD: 3, At: now},
			}}
		}
		return &fakeVenueAvail{ret: va}, true
	})
	return svc, fake
}

// capMapResponse 便捷取 response。
func capMapResponse(ctx context.Context, t *testing.T, svc *Service) *v1.GetCapitalMapResponse {
	t.Helper()
	resp, err := svc.GetCapitalMap(ctx, connect.NewRequest(&v1.GetCapitalMapRequest{}))
	if err != nil {
		t.Fatalf("GetCapitalMap: %v", err)
	}
	return resp.Msg
}

// capTier 按档名取档。
func capTier(resp *v1.GetCapitalMapResponse, tier string) *v1.CapitalMapTier {
	for _, t := range resp.Tiers {
		if t.Tier == tier {
			return t
		}
	}
	return nil
}

// capNodeByLabel 按节点名取节点。
func capNodeByLabel(t *v1.CapitalMapTier, label string) *v1.CapitalMapNode {
	for _, n := range t.Nodes {
		if n.Label == label {
			return n
		}
	}
	return nil
}

// TestD546_CapitalMapTree A2 树完整性：六面全出+三档归类正确（理财活期在「可转」）+折算注记。
func TestD546_CapitalMapTree(t *testing.T) {
	s := covTestStore(t)
	ctx := context.Background()
	// BTCUSDT tick 折算价锚：mid=(50000+50002)/2=50001 → 0.05 BTC = 2500.05 USD。
	if _, err := s.Pool().Exec(ctx, `INSERT INTO ticks (ts, venue, symbol, bid, ask, venue_type)
		VALUES (now(), 'binance', 'BTCUSDT', 50000, 50002, 'spot')`); err != nil {
		t.Fatalf("insert tick: %v", err)
	}
	svc, _ := capMapFixture(t)
	resp := capMapResponse(ctx, t, svc)

	if len(resp.Tiers) != 3 {
		t.Fatalf("tiers = %d, want 3", len(resp.Tiers))
	}
	avail := capTier(resp, "立即可用")
	transfer := capTier(resp, "可转可用")
	locked := capTier(resp, "锁定/慢动")
	if avail == nil || transfer == nil || locked == nil {
		t.Fatalf("三档名缺: %+v", resp.Tiers)
	}

	// 立即可用三面：spot free=100 / perp available=50 / funding=5；合计=155；sizing 注记。
	if n := capNodeByLabel(avail, "binance·现货 可用"); n == nil || !n.HasUsd || n.AmountUsd != 100 {
		t.Errorf("spot free 节点错: %+v", capNodeByLabel(avail, "binance·现货 可用"))
	}
	if n := capNodeByLabel(avail, "binance·永续 可用"); n == nil || n.AmountUsd != 50 {
		t.Errorf("perp available 节点错: %+v", capNodeByLabel(avail, "binance·永续 可用"))
	}
	if n := capNodeByLabel(avail, "binance·资金钱包 余额"); n == nil || n.AmountUsd != 5 {
		t.Errorf("funding 节点错: %+v", capNodeByLabel(avail, "binance·资金钱包 余额"))
	}
	if !avail.HasUsd || avail.TotalUsd != 270 {
		t.Errorf("立即可用合计=%v (has=%v), want 270（binance 155+okx 100+gate 15）", avail.TotalUsd, avail.HasUsd)
	}
	if !strings.Contains(avail.Note, "sizing 基准口径") {
		t.Errorf("立即可用档注记缺 sizing 标注: %q", avail.Note)
	}

	// 可转两面：现货 locked=110−100=10 + 理财活期=30（A2 锚：理财活期不在立即可用）。
	if capNodeByLabel(avail, "binance 理财活期") != nil {
		t.Error("理财活期不得归入立即可用（T+0 赎回≠立即可用）")
	}
	lk := capNodeByLabel(transfer, "binance·现货 锁定")
	if lk == nil || lk.AmountUsd != 10 {
		t.Errorf("spot locked 推导错: %+v", lk)
	}
	if n := capNodeByLabel(transfer, "binance 理财活期"); n == nil || n.AmountUsd != 30 || !n.HasUsd {
		t.Errorf("理财活期节点错: %+v", n)
	}
	if transfer.TotalUsd != 43 {
		t.Errorf("可转合计=%v, want 43（binance 40+okx 3）", transfer.TotalUsd)
	}

	// 锁定档：理财定期=7 + 币本位 BTC 0.05×50001=2500.05 + 期权 617.77（USDT 1:1）。
	if n := capNodeByLabel(locked, "binance 理财定期"); n == nil || n.AmountUsd != 7 {
		t.Errorf("理财定期节点错: %+v", n)
	}
	btc := capNodeByLabel(locked, "binance 币本位 BTC")
	if btc == nil || !btc.HasUsd || math.Abs(btc.AmountUsd-2500.05) > 1e-6 {
		t.Errorf("币本位 BTC 折算错: %+v", btc)
	}
	if btc != nil && !strings.Contains(btc.Note, "BTCUSDT 中间价") {
		t.Errorf("币本位折算注记缺 tick 价来源: %q", btc.Note)
	}
	if n := capNodeByLabel(locked, "binance 期权权益 USDT"); n == nil || n.AmountUsd != 617.77 {
		t.Errorf("期权节点错: %+v", n)
	}
	if resp.Degraded {
		t.Error("全折算场景 degraded 应为 false")
	}
}

// TestD546_CapitalMapUnpricedCoin 缺价币如实标「未折算」不猜（degraded 如实降级）。
func TestD546_CapitalMapUnpricedCoin(t *testing.T) {
	svc, fake := capMapFixture(t)
	fake.ret.CoinM = []reconcile.CoinMBalance{{Asset: "XYZ", Balance: 9, Available: 9}}
	resp := capMapResponse(context.Background(), t, svc)
	locked := capTier(resp, "锁定/慢动")
	n := capNodeByLabel(locked, "binance 币本位 XYZ")
	if n == nil || n.HasUsd {
		t.Fatalf("缺价币应 has_usd=false: %+v", n)
	}
	if !strings.Contains(n.Note, "未折算") {
		t.Errorf("缺价币注记应含「未折算」: %q", n.Note)
	}
	if !resp.Degraded {
		t.Error("缺价未折算应 degraded=true")
	}
}

// TestD546_CapitalMapFetchError 三缺口实拉失败：节点如实标缺不猜零（degraded）。
func TestD546_CapitalMapFetchError(t *testing.T) {
	svc, fake := capMapFixture(t)
	fake.ret = &reconcile.CapitalGaps{Errors: []string{"币本位面: HTTP 500"}, FetchedAt: time.Now().UTC()}
	resp := capMapResponse(context.Background(), t, svc)
	locked := capTier(resp, "锁定/慢动")
	if n := capNodeByLabel(locked, "binance 币本位"); n == nil || n.HasUsd || !strings.Contains(n.Note, "实拉失败") {
		t.Errorf("币本位失败应标缺: %+v", capNodeByLabel(locked, "binance 币本位"))
	}
	if !resp.Degraded {
		t.Error("实拉失败应 degraded=true")
	}
}

// TestD546_CapitalMapCacheZeroREST A4 实拉缓存：60s 窗口内连开页面第二次零 REST（计数断言）。
func TestD546_CapitalMapCacheZeroREST(t *testing.T) {
	svc, fake := capMapFixture(t)
	ctx := context.Background()
	capMapResponse(ctx, t, svc)
	capMapResponse(ctx, t, svc)
	if fake.calls != 1 {
		t.Fatalf("两次页面开应只实拉 1 次（60s 缓存）, got %d", fake.calls)
	}
	svc.capGapsTTLDur = 10 * time.Millisecond // 窗口过 → 重拉
	time.Sleep(20 * time.Millisecond)
	capMapResponse(ctx, t, svc)
	if fake.calls != 2 {
		t.Errorf("缓存过期后应重拉, got %d", fake.calls)
	}
}

// TestD546_CapitalMapNoFetcher 未配置实拉器（无 binance real spot 行/装配不出）：节点如实标缺。
func TestD546_CapitalMapNoFetcher(t *testing.T) {
	svc, _ := capMapFixture(t)
	svc.capGaps = nil
	svc.capGapsCache = nil
	// gradStore fake 无 exchange_configs → 装配不出（gradDB 报错路径）→ gaps=nil。
	resp := capMapResponse(context.Background(), t, svc)
	transfer := capTier(resp, "可转可用")
	if n := capNodeByLabel(transfer, "binance 理财活期"); n == nil || n.HasUsd || !strings.Contains(n.Note, "未采") {
		t.Errorf("无采集器理财活期应标缺: %+v", n)
	}
}

// TestD546_CapitalMapSizingInvariant A3 基准不变锚：全景采集器零进 sizing 路径——
// 同夹具注入计数 fake 跑毕业授权 sizing dry-run，alloc_base 锚值与无采集器基线逐位同值。
func TestD546_CapitalMapSizingInvariant(t *testing.T) {
	snaps := d490Snaps(2000, 1000, "USDT", time.Minute)
	// 基线（无全景采集器）：
	f1, s1 := d490Fixture(t, snaps, 0.01)
	r1, err := s1.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{
		GraduationId: 5, FundingUsd: d490f(200),
	}))
	if err != nil || !r1.Msg.Ok {
		t.Fatalf("基线 sizing: err=%v errstr=%s", err, r1.Msg.Error)
	}
	// 注入计数 fake 后同输入：
	f2, s2 := d490Fixture(t, snaps, 0.01)
	fake := &fakeCapGaps{ret: &reconcile.CapitalGaps{EarnFlexibleUSDT: 99999, EarnLockedUSDT: 99999}}
	s2.SetCapitalGapFetcher(fake)
	r2, err := s2.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{
		GraduationId: 5, FundingUsd: d490f(200),
	}))
	if err != nil || !r2.Msg.Ok {
		t.Fatalf("注入后 sizing: err=%v errstr=%s", err, r2.Msg.Error)
	}
	if fake.calls != 0 {
		t.Errorf("全景采集器被 sizing 路径调用 %d 次——A3 基准被污染", fake.calls)
	}
	if r1.Msg.EstAllocUsd != r2.Msg.EstAllocUsd || r1.Msg.EstGrossUsd != r2.Msg.EstGrossUsd ||
		r1.Msg.EstLegNotionalUsd != r2.Msg.EstLegNotionalUsd {
		t.Errorf("alloc_base 前后不同值: 基线=%v/%v/%v 注入后=%v/%v/%v",
			r1.Msg.EstAllocUsd, r1.Msg.EstGrossUsd, r1.Msg.EstLegNotionalUsd,
			r2.Msg.EstAllocUsd, r2.Msg.EstGrossUsd, r2.Msg.EstLegNotionalUsd)
	}
	if f1.combo == nil || f2.combo == nil || f1.combo.MaxGrossUSD != f2.combo.MaxGrossUSD {
		t.Errorf("combo MaxGrossUSD 前后不同值: %v vs %v",
			f1.combo.MaxGrossUSD, f2.combo.MaxGrossUSD)
	}
}
