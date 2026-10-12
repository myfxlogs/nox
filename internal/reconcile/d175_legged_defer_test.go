// D-175 锚：混合腿 LEGGED 裸露告警延迟判定——登记→Run 尾部惰性复查三分流：
// CLOSED→info「已自动处置（耗时 Xs）」销键；逾期 LEGGED/FAILED→critical「未闭环 N 分钟」销键；
// 窗内 LEGGED/过渡态→零告警续挂。nextBoundary 携登记到期点。
package reconcile

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// deferFake 实现 leggedDeferralStore（reconcile.Store 之外可选面）。
type deferFake struct {
	*fakeStore
	pending map[int64]pgstore.LeggedDeferral
	cleared []int64
	posErr  error
}

func (f *deferFake) ListLeggedDeferrals(ctx context.Context) ([]pgstore.LeggedDeferral, error) {
	out := make([]pgstore.LeggedDeferral, 0, len(f.pending))
	for _, d := range f.pending {
		out = append(out, d)
	}
	return out, nil
}

func (f *deferFake) ClearLeggedDeferral(ctx context.Context, posID int64) error {
	delete(f.pending, posID)
	f.cleared = append(f.cleared, posID)
	return nil
}

func (f *deferFake) GetPosition(ctx context.Context, id int64) (*pgstore.Position, error) {
	if f.posErr != nil {
		return nil, f.posErr
	}
	p := f.positions[id]
	if p == nil {
		return &pgstore.Position{ID: id, Status: "MISSING"}, nil
	}
	return p, nil
}

func (f *deferFake) InsertPositionAlert(ctx context.Context, posID int64, level, msg string) error {
	f.alerts = append(f.alerts, fakeAlert{posID: &posID, level: level, msg: msg})
	return nil
}

func newDeferRecon(fs *deferFake) *Reconciler {
	r := New(fs)
	r.runTimeout = time.Second
	return r
}

// 自愈闭环：LEGGED 登记后 position CLOSED → info「已自动处置（耗时 Xs）」一条+销键。
func TestD175_DeferredClosedInfo(t *testing.T) {
	fs := &deferFake{fakeStore: newFakeStore(), pending: map[int64]pgstore.LeggedDeferral{}}
	since := time.Now().Add(-40 * time.Second)
	closed := time.Now()
	fs.positions[900] = &pgstore.Position{ID: 900, Status: "CLOSED", ClosedAt: &closed}
	fs.pending[900] = pgstore.LeggedDeferral{PosID: 900, Filled: 1, Total: 2, Since: since}

	r := newDeferRecon(fs)
	r.evalLeggedDeferrals(context.Background())

	if len(fs.pending) != 0 || len(fs.cleared) != 1 || fs.cleared[0] != 900 {
		t.Fatalf("CLOSED 应销键, cleared=%v pending=%v", fs.cleared, fs.pending)
	}
	if len(fs.alerts) != 1 || fs.alerts[0].level != "info" {
		t.Fatalf("应恰一 info 降级行, got %v", fs.alerts)
	}
	if want := "已自动处置（耗时"; !contains(fs.alerts[0].msg, want) {
		t.Fatalf("info 文案应携耗时, got %q", fs.alerts[0].msg)
	}
}

// 逾期未闭环：LEGGED 超窗 → critical「自动处置未闭环 N 分钟——需人工介入」+销键（单次宣判）。
func TestD175_DeferredOverdueCritical(t *testing.T) {
	fs := &deferFake{fakeStore: newFakeStore(), pending: map[int64]pgstore.LeggedDeferral{}}
	fs.positions[901] = &pgstore.Position{ID: 901, Status: "LEGGED"}
	fs.pending[901] = pgstore.LeggedDeferral{PosID: 901, Filled: 1, Total: 2, Since: time.Now().Add(-leggedDeferWindow - time.Minute)}

	r := newDeferRecon(fs)
	r.evalLeggedDeferrals(context.Background())

	if len(fs.alerts) != 1 || fs.alerts[0].level != "critical" {
		t.Fatalf("应恰一 critical, got %v", fs.alerts)
	}
	if want := "自动处置未闭环"; !contains(fs.alerts[0].msg, want) || !contains(fs.alerts[0].msg, "需人工介入") {
		t.Fatalf("critical 文案应携未闭环+人工介入, got %q", fs.alerts[0].msg)
	}
	if len(fs.pending) != 0 {
		t.Fatalf("宣判后应销键（episode 下游限重）, pending=%v", fs.pending)
	}
}

// 窗内续挂：LEGGED 未超窗 → 零告警、登记保留。
func TestD175_DeferredWithinWindow(t *testing.T) {
	fs := &deferFake{fakeStore: newFakeStore(), pending: map[int64]pgstore.LeggedDeferral{}}
	fs.positions[902] = &pgstore.Position{ID: 902, Status: "LEGGED"}
	fs.pending[902] = pgstore.LeggedDeferral{PosID: 902, Filled: 1, Total: 2, Since: time.Now()}

	r := newDeferRecon(fs)
	r.evalLeggedDeferrals(context.Background())

	if len(fs.alerts) != 0 {
		t.Fatalf("窗内应零告警, got %v", fs.alerts)
	}
	if len(fs.pending) != 1 {
		t.Fatalf("窗内登记应续挂, pending=%v", fs.pending)
	}
}

// nextBoundary：登记存在时携 since+window 到期点；无登记不影响既有边界。
func TestD175_DeferralBoundary(t *testing.T) {
	fs := &deferFake{fakeStore: newFakeStore(), pending: map[int64]pgstore.LeggedDeferral{}}
	since := time.Now()
	fs.pending[903] = pgstore.LeggedDeferral{PosID: 903, Filled: 1, Total: 2, Since: since}

	r := newDeferRecon(fs)
	b, ok := r.leggedDeferralBoundary(context.Background())
	if !ok || !b.Equal(since.Add(leggedDeferWindow)) {
		t.Fatalf("boundary 应=since+window, got %v ok=%v", b, ok)
	}
	fs.pending = nil
	if _, ok = r.leggedDeferralBoundary(context.Background()); ok {
		t.Fatal("空登记应 ok=false")
	}
}
