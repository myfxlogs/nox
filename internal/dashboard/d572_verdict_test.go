package dashboard

// d572_verdict_test.go —— D-572 验收锚 A1-A4（腿池杠杆实读透出+读穿缓存+三态不变）。
import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// d572Svc 腿池判定夹具：gate 池=活账本（avail 可调）；MT5 池=快照+init_margin_rate fact；
// qbus 注入 BTCUSDT@gate:perp VenueSymbol=BTC_USDT。
func d572Svc(t *testing.T, gateAvail float64, lev float64, levErr error) (*Service, *atomic.Int32) {
	t.Helper()
	s := d563VerdictSvc(t, gateAvail, gateAvail*2)
	f := s.gradStore.(*fakeGradDB)
	f.facts = map[string][]fact.Fact{
		"3001157|": {{Kind: fact.KindInitMarginRate, Venue: "3001157", Value: 0.02, Ts: time.Now()}},
	}
	s.qbus = bus.New()
	s.qbus.Publish(bus.Quote{Symbol: "BTCUSDT", Venue: "gate", VenueType: "perp", VenueSymbol: "BTC_USDT", Bid: 100, Ask: 100.1})
	var calls atomic.Int32
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) {
		calls.Add(1)
		return lev, levErr
	}
	return s, &calls
}

func d572Combo() (pgstore.XAssetComboAdminRow, []pgstore.VenueAccountLatest) {
	notional := 4300.0
	c := pgstore.XAssetComboAdminRow{
		CanonicalBuy: "BTCUSDT", CanonicalSell: "BTCUSDT",
		VenueBuy: "gate:perp", VenueSell: "3001157:mt5", NotionalUSD: &notional,
	}
	return c, []pgstore.VenueAccountLatest{d563MtSnap(5000, 4000)}
}

// A1：perp 实读透出——lev=10 → frac=0.1；need×0.1>avail→conflict 文案含杠杆+时刻；
// avail 充足→ok。
func TestD572_A1_PerpLeverageLive(t *testing.T) {
	ctx := context.Background()
	// avail=400：need 4300×0.1=430>400 → conflict。
	s, _ := d572Svc(t, 400, 10, nil)
	c, snaps := d572Combo()
	if v, d := s.comboPoolVerdict(ctx, c, snaps); v != "conflict" ||
		!strings.Contains(d, "率 0.100") || !strings.Contains(d, "gate:perp") {
		t.Fatalf("430>400 应 conflict: %s %s", v, d)
	}
	// avail=5000：430<5000 → ok；detail 空——直接验 frac/note 透出真值+读时刻。
	s2, _ := d572Svc(t, 5000, 10, nil)
	if v, _ := s2.comboPoolVerdict(ctx, c, snaps); v != "ok" {
		t.Fatalf("430<5000 应 ok: %s", v)
	}
	frac, note, known := s2.legMarginNeedFrac(ctx, "gate:perp", "BTC_USDT", false)
	if !known || frac != 0.1 || !strings.Contains(note, "10×") || !strings.Contains(note, "实读") {
		t.Fatalf("frac/note 透出错: %v %q %v", frac, note, known)
	}
}

// A2：读败诚实——fake 返 error → unknown 黄标（note=杠杆实况不可得）不猜数。
func TestD572_A2_ReadFailUnknown(t *testing.T) {
	s, _ := d572Svc(t, 5000, 0, errors.New("venue timeout"))
	c, snaps := d572Combo()
	if v, d := s.comboPoolVerdict(context.Background(), c, snaps); v != "unknown" ||
		!strings.Contains(d, "杠杆实况不可得") {
		t.Fatalf("读败应 unknown 黄标不猜: %s %s", v, d)
	}
}

// A3：读穿缓存——同 (vk,sym,side) 5min 内二次调用计数=1；陈条目超窗重取=2；
// note 时刻=缓存 at。
func TestD572_A3_ReadThroughCache(t *testing.T) {
	ctx := context.Background()
	s, calls := d572Svc(t, 5000, 10, nil)
	_, n1, _ := s.legMarginNeedFrac(ctx, "gate:perp", "BTC_USDT", false)
	_, n2, _ := s.legMarginNeedFrac(ctx, "gate:perp", "BTC_USDT", false)
	if calls.Load() != 1 {
		t.Fatalf("5min 内二次调用应缓存命中（计数=1）: %d", calls.Load())
	}
	if n1 != n2 {
		t.Fatalf("命中 note 时刻应=缓存 at（同一字符串）: %q vs %q", n1, n2)
	}
	// 人工造 >5min 陈条目 → 重取。
	s.levCacheMu.Lock()
	e := s.levCache["gate:perp|BTC_USDT|buy"]
	e.at = time.Now().Add(-6 * time.Minute)
	s.levCache["gate:perp|BTC_USDT|buy"] = e
	s.levCacheMu.Unlock()
	if _, _, known := s.legMarginNeedFrac(ctx, "gate:perp", "BTC_USDT", false); !known || calls.Load() != 2 {
		t.Fatalf("超窗应重取（计数=2）: %d", calls.Load())
	}
	// 侧异键（sell）不共享缓存。
	if _, _, _ = s.legMarginNeedFrac(ctx, "gate:perp", "BTC_USDT", true); calls.Load() != 3 {
		t.Fatalf("sell 侧应独立键重取: %d", calls.Load())
	}
}

// A4：边界不动——MT5 腿仍走 facts 路径（venueLeverageFn 零调用）；
// VenueSymbol 取不到 → unknown 不猜符号。
func TestD572_A4_MTUntouched(t *testing.T) {
	ctx := context.Background()
	s, calls := d572Svc(t, 5000, 10, nil)
	frac, _, known := s.legMarginNeedFrac(ctx, "3001157:mt5", "", false)
	if !known || frac != 0.02 || calls.Load() != 0 {
		t.Fatalf("MT 应走 facts 零 venueLeverage 调用: frac=%v calls=%d", frac, calls.Load())
	}
	// qbus 无该 canonical 报价 → sym 空串进实读（venue 侧自然读败=unknown 不猜）。
	s.qbus = bus.New() // 空 bus
	s.venueLeverageFn = func(_ context.Context, _, sym string, _ bool) (float64, error) {
		calls.Add(1)
		if sym != "" {
			return 0, errors.New("sym 应空串")
		}
		return 0, errors.New("venue 读败")
	}
	c, snaps := d572Combo()
	if v, d := s.comboPoolVerdict(ctx, c, snaps); v != "unknown" {
		t.Fatalf("sym 空读败应 unknown 不猜: %s %s", v, d)
	}
	if calls.Load() == 0 {
		t.Fatalf("perp 腿应经 venueLeverage（空 sym 传通）")
	}
}
