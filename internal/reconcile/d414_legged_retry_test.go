// d414_legged_retry_test.go —— D-414 S1：LEGGED 残腿自动回收测试矩阵。
// T1 LEGGED 仓被收编（ClaimClose→ResumeClose）②uncertainty 红线跳过
// T3 ErrClaimFailed 仍走 ResumeClose 幂等 T4 哨兵/计数语义与 CLOSING 同口径。
package reconcile

import (
	"context"
	"errors"
	"sync"
	"testing"

	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// d414Closer Closer fake：分别记录 ClaimClose/ResumeClose 调用，可注入错误。
type d414Closer struct {
	mu          sync.Mutex
	claimCalls  []int64
	resumeCalls []int64
	claimErr    error
	resumeErr   error
}

func (c *d414Closer) ClaimClose(ctx context.Context, posID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.claimCalls = append(c.claimCalls, posID)
	return c.claimErr
}
func (c *d414Closer) ResumeClose(ctx context.Context, posID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resumeCalls = append(c.resumeCalls, posID)
	return c.resumeErr
}
func (c *d414Closer) AutoClosePosition(ctx context.Context, posID int64) error { return nil }
func (c *d414Closer) counts() (claim, resume int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.claimCalls), len(c.resumeCalls)
}

// d414LeggedPos 构造 LEGGED 仓（uncertainty 可控）+ FILLED 残腿（venue 归因）。
func d414LeggedPos(id int64, uncertain string, venues ...string) (*pgstore.Position, []*pgstore.PositionLeg) {
	p := &pgstore.Position{ID: id, OpportunityID: "opp:x", Type: "XASSET_CARRY", Status: "LEGGED"}
	if uncertain != "" {
		p.UncertaintyReason = &uncertain
	}
	var legs []*pgstore.PositionLeg
	for i, v := range venues {
		legs = append(legs, &pgstore.PositionLeg{
			PositionID: id, LegNo: int16(i + 1), Phase: "OPEN", Venue: v,
			Symbol: "XAUUSD", Side: "BUY", Qty: 1, Status: "FILLED",
		})
	}
	return p, legs
}

// TestD414_LeggedClaimed T1：LEGGED 仓 → sweep 收编——先 ClaimClose 再 ResumeClose。
func TestD414_LeggedClaimed(t *testing.T) {
	fs := newFakeStore()
	c := &d414Closer{}
	r := New(fs)
	r.closer = c
	p, legs := d414LeggedPos(1, "", "v1:mt5")
	fs.positions[1] = p
	fs.legs[1] = legs
	r.fastRetrySweep(context.Background())
	claim, resume := c.counts()
	if claim != 1 || resume != 1 {
		t.Fatalf("LEGGED 应 ClaimClose+ResumeClose 各 1 次, claim=%d resume=%d", claim, resume)
	}
}

// TestD414_LeggedUncertainSkipped T2：uncertainty_reason 非空 → closer 零调用（D-189 红线）。
func TestD414_LeggedUncertainSkipped(t *testing.T) {
	fs := newFakeStore()
	c := &d414Closer{}
	r := New(fs)
	r.closer = c
	p, legs := d414LeggedPos(1, "uncertain: venue timeout", "v1:mt5")
	fs.positions[1] = p
	fs.legs[1] = legs
	r.fastRetrySweep(context.Background())
	claim, resume := c.counts()
	if claim != 0 || resume != 0 {
		t.Fatalf("UNCERTAIN LEGGED 不得自动收编, claim=%d resume=%d", claim, resume)
	}
}

// TestD414_LeggedClaimFailedStillResume T3：ClaimClose 返 ErrClaimFailed（并发已收编）
// → 仍走 ResumeClose 幂等，不计 venue 失败。
func TestD414_LeggedClaimFailedStillResume(t *testing.T) {
	fs := newFakeStore()
	c := &d414Closer{claimErr: pgstore.ErrClaimFailed}
	r := New(fs)
	r.closer = c
	p, legs := d414LeggedPos(1, "", "v1:mt5")
	fs.positions[1] = p
	fs.legs[1] = legs
	r.fastRetrySweep(context.Background())
	claim, resume := c.counts()
	if claim != 1 || resume != 1 {
		t.Fatalf("ErrClaimFailed 后仍须 ResumeClose 幂等, claim=%d resume=%d", claim, resume)
	}
	if r.fastRetryStreak["v1:mt5"] != 0 {
		t.Errorf("ErrClaimFailed→ResumeClose nil 不得计数, streak=%d", r.fastRetryStreak["v1:mt5"])
	}
}

// TestD414_LeggedErrorSemantics T4：ResumeClose ErrCloseInFlight→不计数；一般 err→计数+1。
func TestD414_LeggedErrorSemantics(t *testing.T) {
	fs := newFakeStore()
	c := &d414Closer{resumeErr: exec.ErrCloseInFlight}
	r := New(fs)
	r.closer = c
	p, legs := d414LeggedPos(1, "", "v1:mt5")
	fs.positions[1] = p
	fs.legs[1] = legs
	r.fastRetrySweep(context.Background())
	if r.fastRetryStreak["v1:mt5"] != 0 {
		t.Fatalf("ErrCloseInFlight 不得计数, streak=%d", r.fastRetryStreak["v1:mt5"])
	}
	c.mu.Lock()
	c.resumeErr = errors.New("mt5 orderclose REQUEST_ERROR")
	c.mu.Unlock()
	r.fastRetrySweep(context.Background())
	if r.fastRetryStreak["v1:mt5"] != 1 {
		t.Fatalf("一般 err 应计数+1, streak=%d", r.fastRetryStreak["v1:mt5"])
	}
}

// TestD414_LeggedClaimOtherErrCounted T4b：ClaimClose 其他 err → log+计数，不走 ResumeClose。
func TestD414_LeggedClaimOtherErrCounted(t *testing.T) {
	fs := newFakeStore()
	c := &d414Closer{claimErr: errors.New("db down")}
	r := New(fs)
	r.closer = c
	p, legs := d414LeggedPos(1, "", "v1:mt5")
	fs.positions[1] = p
	fs.legs[1] = legs
	r.fastRetrySweep(context.Background())
	claim, resume := c.counts()
	if claim != 1 || resume != 0 {
		t.Fatalf("ClaimClose 其他 err 不得继续 ResumeClose, claim=%d resume=%d", claim, resume)
	}
	if r.fastRetryStreak["v1:mt5"] != 1 {
		t.Errorf("ClaimClose 其他 err 应计数+1, streak=%d", r.fastRetryStreak["v1:mt5"])
	}
}
