// gate_test.go —— okx 共额退避闸锚：任一面 429→全局罚窗罩住其他面；
// 窗内直返 ErrThrottled 不发不占；窗到期放行；成功减半衰减。
package okxrate

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestSharedPenaltyCoversAllPaths(t *testing.T) {
	gate.Lock()
	gate.paths = map[string]*pathGate{}
	gate.penalty, gate.globalUntil = 0, time.Time{}
	gate.Unlock()

	ctx := context.Background()
	// 面 A（earn）撞 429 → 共额罚窗。
	Result(fmt.Errorf("HTTP 429: {\"code\":\"50011\"}"))
	// 面 B（balance 异端点）同被罩——共额语义核心锚。
	if err := Wait(ctx, "keyB|/api/v5/account/balance"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("异端点应被共额罚窗罩住: %v", err)
	}
	if err := Wait(ctx, "keyA|/api/v5/finance/savings/balance"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("原端点同被罩: %v", err)
	}
	// 罚窗到期自然放行。
	gate.Lock()
	gate.globalUntil = time.Now().Add(-time.Second)
	gate.Unlock()
	if err := Wait(ctx, "keyB|/api/v5/account/balance"); err != nil {
		t.Fatalf("罚窗到期应放行: %v", err)
	}
}

func TestIs429(t *testing.T) {
	for _, s := range []string{
		"okx balance: http 429 {\"code\":\"50011\"}",
		"HTTP 429: {\"msg\":\"Too many requests\"}",
		"okx dcd: code=50011 too many",
	} {
		if !Is429(errors.New(s)) {
			t.Fatalf("应判 429: %s", s)
		}
	}
	if Is429(errors.New("HTTP 500")) || Is429(nil) {
		t.Fatal("非 429 误判")
	}
}

func resetGate(min, backoff, max time.Duration) {
	gate.Lock()
	defer gate.Unlock()
	gate.paths = map[string]*pathGate{}
	gate.min, gate.backoff, gate.max = min, backoff, max
	gate.penalty, gate.globalUntil = 0, time.Time{}
}

func TestGate_SpacingSerializes(t *testing.T) {
	resetGate(50*time.Millisecond, time.Second, 10*time.Second)
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := Wait(ctx, "k|/p"); err != nil {
			t.Fatalf("wait %d: %v", i, err)
		}
	}
	if el := time.Since(start); el < 100*time.Millisecond {
		t.Fatalf("三次连发应被拉距 ≥100ms，实测 %v", el)
	}
	if err := Wait(ctx, "k|/q"); err != nil {
		t.Fatalf("异端点应即时放行: %v", err)
	}
}

func TestGate_429SharedWindowEscalates(t *testing.T) {
	resetGate(20*time.Millisecond, 500*time.Millisecond, 2*time.Second)
	ctx := context.Background()
	if err := Wait(ctx, "k|/p"); err != nil {
		t.Fatal(err)
	}
	Result(fmt.Errorf("http 429"))
	if err := Wait(ctx, "k|/q"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("共额罚窗应罩异端点: %v", err)
	}
	// 撞单再叠：罚量倍增→窗更长。
	Result(fmt.Errorf("http 429"))
	time.Sleep(600 * time.Millisecond)
	if err := Wait(ctx, "k|/p"); err == nil {
		t.Fatal("次罚窗内仍应 skip")
	}
}

func TestGate_SuccessDecaysPenalty(t *testing.T) {
	resetGate(20*time.Millisecond, 400*time.Millisecond, 2*time.Second)
	Result(fmt.Errorf("http 429")) // penalty=400
	Result(nil)
	gate.Lock()
	p := gate.penalty
	gate.Unlock()
	if p != 200*time.Millisecond {
		t.Fatalf("成功减半衰减: penalty=%v", p)
	}
}

func TestGate_Non429NoPenalty(t *testing.T) {
	resetGate(20*time.Millisecond, time.Second, 10*time.Second)
	Result(fmt.Errorf("http 500 venue 故障"))
	gate.Lock()
	p := gate.penalty
	gate.Unlock()
	if p != 0 {
		t.Fatalf("非 429 不加罚: %v", p)
	}
}

func TestGate_CtxCancel(t *testing.T) {
	resetGate(200*time.Millisecond, time.Second, 10*time.Second)
	Wait(context.Background(), "k|/p")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := Wait(ctx, "k|/p"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等待应随 ctx 取消: %v", err)
	}
}
