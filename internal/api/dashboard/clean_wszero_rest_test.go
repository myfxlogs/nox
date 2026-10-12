package dashboard

// D-542 验收锚：A1 流主源/脏标 REST 校准/快照兜底、A3 30s 缓存单发、
// A4 敞口闸未设行显式透出。单测=legPool/unsetCapKeys 层（REST 调用面=liveQuerierFor 注入）。

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/ledger/reconcile"
	"nox/internal/store/pgstore"
)

// fakeAcctQ 计数假 AccountQuerier（A1/A3 REST 调用面钉死）。
type fakeAcctQ struct {
	n    atomic.Int32
	acct *reconcile.AccountInfo
	err  error
	hold chan struct{} // 非 nil=阻塞 Account（并发单飞演练）
}

func (f *fakeAcctQ) Account(ctx context.Context) (*reconcile.AccountInfo, error) {
	f.n.Add(1)
	if f.hold != nil {
		<-f.hold
	}
	return f.acct, f.err
}
func (f *fakeAcctQ) VenueKey() string { return "binance:spot" }

func d542Svc(q *fakeAcctQ) (*Service, *bus.BalanceLedger) {
	s := &Service{}
	l := bus.NewBalanceLedger()
	s.SetBalanceLedger(l)
	s.liveQuerierFor = func(context.Context, string) (reconcile.AccountQuerier, string, bool) {
		if q == nil {
			return nil, "", false
		}
		return q, "binance:spot", true
	}
	return s, l
}

// A1① 干净 ws 条目直读=零 REST（流主源钉死：Put 后 Get 命中，querier 计数恒 0）。
func TestD542_CleanWSZeroREST(t *testing.T) {
	q := &fakeAcctQ{acct: &reconcile.AccountInfo{FreeMargin: 500, Equity: 600, Currency: "USD"}}
	s, l := d542Svc(q)
	l.Put("binance:spot", 800, 1000, "USD", "ws", time.Now())
	p, ok := s.legPool(context.Background(), nil, "binance:spot")
	if !ok || !p.live || p.src != "ws" || *p.free != 800 {
		t.Fatalf("pool=%+v ok=%v", p, ok)
	}
	if q.n.Load() != 0 {
		t.Fatalf("REST calls=%d want 0（干净流主源直读）", q.n.Load())
	}
}

// A1② 重连标脏 → 下一读触发一发 REST 校准并回写账本（src=rest）。
func TestD542_DirtyCalibrates(t *testing.T) {
	q := &fakeAcctQ{acct: &reconcile.AccountInfo{FreeMargin: 500, Equity: 600, Currency: "USD"}}
	s, l := d542Svc(q)
	l.Put("binance:spot", 800, 1000, "USD", "ws", time.Now())
	l.MarkDirty("binance:spot") // 私有流重连
	p, ok := s.legPool(context.Background(), nil, "binance:spot")
	if !ok || !p.live || p.src != "rest" || *p.free != 500 {
		t.Fatalf("pool=%+v ok=%v", p, ok)
	}
	if q.n.Load() != 1 {
		t.Fatalf("REST calls=%d want 1", q.n.Load())
	}
}

// A3 30s 缓存：TTL 内两次弹窗读=一次 REST。
func TestD542_RestCacheTTL(t *testing.T) {
	q := &fakeAcctQ{acct: &reconcile.AccountInfo{FreeMargin: 500, Equity: 600, Currency: "USD"}}
	s, _ := d542Svc(q)
	s.liveRestTTLDur = 60 * time.Second
	for i := 0; i < 2; i++ {
		if _, ok := s.legPool(context.Background(), nil, "binance:spot"); !ok {
			t.Fatalf("read %d miss", i)
		}
	}
	if q.n.Load() != 1 {
		t.Fatalf("REST calls=%d want 1（30s 缓存）", q.n.Load())
	}
	// TTL 过期=再读重拉。
	s.liveRestTTLDur = time.Nanosecond
	time.Sleep(time.Millisecond)
	if _, ok := s.legPool(context.Background(), nil, "binance:spot"); !ok {
		t.Fatal("expired read miss")
	}
	if q.n.Load() != 2 {
		t.Fatalf("REST calls=%d want 2（TTL 过期重拉）", q.n.Load())
	}
}

// A1③ 并发开窗单飞：10 读并发=一次 REST（liveMu 复查钳）。
func TestD542_ConcurrentSingleflight(t *testing.T) {
	q := &fakeAcctQ{
		acct: &reconcile.AccountInfo{FreeMargin: 500, Equity: 600, Currency: "USD"},
		hold: make(chan struct{}),
	}
	s, _ := d542Svc(q)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.legPool(context.Background(), nil, "binance:spot") }()
	}
	time.Sleep(30 * time.Millisecond)
	close(q.hold) // 放行校准
	wg.Wait()
	if q.n.Load() != 1 {
		t.Fatalf("REST calls=%d want 1（并发单飞）", q.n.Load())
	}
}

// A1④ REST 败 → 快照兜底（live=false、源注记空走快照口径，快照龄闸照旧生效）。
func TestD542_RestFailSnapFallback(t *testing.T) {
	q := &fakeAcctQ{err: errors.New("proxy dead")}
	s, _ := d542Svc(q)
	eq := 700.0
	snaps := []pgstore.VenueAccountLatest{
		{Venue: "binance", VenueType: "spot", EquityRaw: &eq, Currency: "USD", FetchedAt: time.Now()},
	}
	p, ok := s.legPool(context.Background(), snaps, "binance:spot")
	if !ok || p.live || p.equity != 700 {
		t.Fatalf("pool=%+v ok=%v", p, ok)
	}
	if q.n.Load() != 1 {
		t.Fatalf("REST calls=%d want 1", q.n.Load())
	}
}

// A4 未设闸显式：账户面存在但闸表无行 → unset 键序透出（binance:spot/perp 空表钉死）。
func TestD542_UnsetCapKeys(t *testing.T) {
	got := unsetCapKeysOf(
		[]string{"binance:spot", "binance:perp", "okx:unified", "3001157:mt5", "binance:spot"},
		map[string]pgstore.VenueExposureCap{"okx:unified": {VenueKey: "okx:unified", CapUSD: 500, Enabled: true}},
	)
	want := []string{"3001157:mt5", "binance:perp", "binance:spot"}
	if len(got) != len(want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got=%v want=%v", got, want)
		}
	}
}
