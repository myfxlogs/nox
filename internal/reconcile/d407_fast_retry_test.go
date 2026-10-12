// d407_fast_retry_test.go —— D-407 S3：残腿密集重试驱动测试矩阵。
// ①UNCERTAIN 仓不重试（红线）②窗口内重试/窗外交还③单飞锁防并发（exec 包入口断言）
// ④venue 计数达阈升级告警⑤残腿清零后停止。
package reconcile

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// d407Closer Closer fake：记录 ResumeClose 调用次数，可注入错误/成功置 CLOSED。
type d407Closer struct {
	mu         sync.Mutex
	calls      []int64
	err        error
	markClosed bool // ResumeClose 成功时把 fakeStore 仓置 CLOSED（模拟真平掉）
	store      *fakeStore
}

func (c *d407Closer) ResumeClose(ctx context.Context, posID int64) error {
	c.mu.Lock()
	c.calls = append(c.calls, posID)
	err := c.err
	c.mu.Unlock()
	if err == nil && c.markClosed && c.store != nil {
		c.store.ClosePosition(ctx, posID, 0, nil, "")
	}
	return err
}
func (c *d407Closer) AutoClosePosition(ctx context.Context, posID int64) error { return nil }
func (c *d407Closer) ClaimClose(ctx context.Context, posID int64) error        { return nil }
func (c *d407Closer) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

// d407ClosingPos 构造 CLOSING 仓（closing_started_at/uncertainty_reason 可控）+ 残腿。
func d407ClosingPos(id int64, startedAgo time.Duration, uncertain string, venues ...string) (*pgstore.Position, []*pgstore.PositionLeg) {
	now := time.Now()
	cs := now.Add(-startedAgo)
	p := &pgstore.Position{ID: id, OpportunityID: "opp:x", Type: "XASSET_CARRY", Status: "CLOSING", ClosingStartedAt: &cs}
	if uncertain != "" {
		p.UncertaintyReason = &uncertain
	}
	var legs []*pgstore.PositionLeg
	for i, v := range venues {
		legs = append(legs, &pgstore.PositionLeg{
			PositionID: id, LegNo: int16(i + 1), Phase: "OPEN", Venue: v,
			Symbol: "XPTUSD", Side: "BUY", Qty: 1, Status: "FILLED",
		})
	}
	return p, legs
}

func d407Recon(fs *fakeStore, c *d407Closer) *Reconciler {
	r := New(fs)
	r.closer = c
	return r
}

// TestD407_UncertainNeverRetried ①红线：uncertainty_reason 非空 → 窗内也绝不重试。
func TestD407_UncertainNeverRetried(t *testing.T) {
	fs := newFakeStore()
	c := &d407Closer{}
	r := d407Recon(fs, c)
	p, legs := d407ClosingPos(1, time.Minute, "uncertain: partial fill", "v1:mt5")
	fs.positions[1] = p
	fs.legs[1] = legs
	r.fastRetrySweep(context.Background())
	if n := c.callCount(); n != 0 {
		t.Errorf("UNCERTAIN 仓不得重试（盲平=反向开新仓）, calls=%d", n)
	}
}

// TestD407_WindowGate ②窗内重试 / 窗外与 nil ClosingStartedAt 交还看护。
func TestD407_WindowGate(t *testing.T) {
	fs := newFakeStore()
	c := &d407Closer{}
	r := d407Recon(fs, c)
	inWin, legsIn := d407ClosingPos(1, 5*time.Minute, "", "v1:mt5")
	outWin, legsOut := d407ClosingPos(2, 11*time.Minute, "", "v2:mt5")
	nilStart, legsNil := d407ClosingPos(3, 0, "", "v3:mt5")
	nilStart.ClosingStartedAt = nil
	fs.positions[1], fs.positions[2], fs.positions[3] = inWin, outWin, nilStart
	fs.legs[1], fs.legs[2], fs.legs[3] = legsIn, legsOut, legsNil
	r.fastRetrySweep(context.Background())
	if n := c.callCount(); n != 1 {
		t.Fatalf("窗内仓应重试 1 次, calls=%d", n)
	}
	if c.calls[0] != 1 {
		t.Errorf("应只重试窗内仓 pos=1, got %v", c.calls)
	}
}

// TestD407_InFlightNotCounted 单飞哨兵：ErrCloseInFlight → 可重入不告警不计数。
func TestD407_InFlightNotCounted(t *testing.T) {
	fs := newFakeStore()
	c := &d407Closer{err: exec.ErrCloseInFlight}
	r := d407Recon(fs, c)
	p, legs := d407ClosingPos(1, time.Minute, "", "v1:mt5")
	fs.positions[1] = p
	fs.legs[1] = legs
	for i := 0; i < 3; i++ {
		r.fastRetrySweep(context.Background())
	}
	if len(fs.alerts) != 0 {
		t.Errorf("ErrCloseInFlight 不得产生告警, alerts=%v", fs.alerts)
	}
	if r.fastRetryStreak["v1:mt5"] != 0 {
		t.Errorf("ErrCloseInFlight 不得计数, streak=%d", r.fastRetryStreak["v1:mt5"])
	}
}

// TestD407_VenueFailEscalation ④venue 连续失败达 consecutiveN（默认 3）→ warn 升级告警；
// 告警后计数清零（节流，再达阈才再报）。
func TestD407_VenueFailEscalation(t *testing.T) {
	fs := newFakeStore()
	c := &d407Closer{err: errors.New("mt5 orderclose REQUEST_ERROR")}
	r := d407Recon(fs, c)
	p, legs := d407ClosingPos(1, time.Minute, "", "v1:mt5")
	fs.positions[1] = p
	fs.legs[1] = legs
	for i := 0; i < 3; i++ {
		r.fastRetrySweep(context.Background())
	}
	if len(fs.alerts) != 1 || fs.alerts[0].level != "warn" {
		t.Fatalf("达阈应产生 1 条 warn 升级告警, alerts=%v", fs.alerts)
	}
	if got := fs.alerts[0].msg; !containsAll(got, "v1:mt5", "备用通道待业主裁") {
		t.Errorf("告警文本须含 venue 与备用通道待裁提示, got %q", got)
	}
	if r.fastRetryStreak["v1:mt5"] != 0 {
		t.Error("告警后 streak 应清零（节流）")
	}
	// 再失败 2 次（未达阈）→ 无新增告警
	for i := 0; i < 2; i++ {
		r.fastRetrySweep(context.Background())
	}
	if len(fs.alerts) != 1 {
		t.Errorf("未再达阈不得重复告警, alerts=%d", len(fs.alerts))
	}
}

// TestD407_ResidualGoneStops ⑤残腿清零：ResumeClose 成功平仓 → 下轮无 CLOSING → 停止调用；
// venue 失败计数清零。
func TestD407_ResidualGoneStops(t *testing.T) {
	fs := newFakeStore()
	c := &d407Closer{markClosed: true, store: fs}
	r := d407Recon(fs, c)
	p, legs := d407ClosingPos(1, time.Minute, "", "v1:mt5")
	fs.positions[1] = p
	fs.legs[1] = legs
	r.fastRetrySweep(context.Background())
	if n := c.callCount(); n != 1 {
		t.Fatalf("首轮应重试, calls=%d", n)
	}
	r.fastRetrySweep(context.Background())
	if n := c.callCount(); n != 1 {
		t.Errorf("仓已 CLOSED 不得再调用, calls=%d", n)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
