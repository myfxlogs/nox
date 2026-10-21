// okx_rate_gate_test.go —— okx 签名 GET 节流闸：间距序列化、429 倍增退避、
// 罚期 skip 不占位、成功减半衰减、ctx 取消。
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func resetOkxGate(min, backoff, max time.Duration) {
	okxGate.Lock()
	defer okxGate.Unlock()
	okxGate.paths = map[string]*okxPathGate{}
	okxGate.min, okxGate.backoff, okxGate.max = min, backoff, max
}

func TestOkxGate_SpacingSerializes(t *testing.T) {
	resetOkxGate(50*time.Millisecond, time.Second, 10*time.Second)
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := okxGateWait(ctx, "k|/p"); err != nil {
			t.Fatalf("wait %d: %v", i, err)
		}
	}
	if el := time.Since(start); el < 100*time.Millisecond {
		t.Fatalf("三次连发应被拉距 ≥100ms，实测 %v", el)
	}
	// 不同键（端点/凭据）互不牵制。
	if err := okxGateWait(ctx, "k|/q"); err != nil {
		t.Fatalf("异端点应即时放行: %v", err)
	}
}

func TestOkxGate_429BackoffEscalatesAndSkips(t *testing.T) {
	resetOkxGate(20*time.Millisecond, 500*time.Millisecond, 2*time.Second)
	ctx := context.Background()
	k := "k|/p"
	if err := okxGateWait(ctx, k); err != nil {
		t.Fatal(err)
	}
	okxGateResult(k, fmt.Errorf("okx: http 429 {\"code\":\"50011\"}"))
	// 罚期（min+penalty=520ms）内：skip 即返不占位。
	err := okxGateWait(ctx, k)
	if !errors.Is(err, ErrOkxThrottled) {
		t.Fatalf("罚期应 skip 报 ErrOkxThrottled: %v", err)
	}
	// skip 不占时隙——下轮仍 skip（窗未过）。
	if err := okxGateWait(ctx, k); !errors.Is(err, ErrOkxThrottled) {
		t.Fatalf("连 skip 语义: %v", err)
	}
	// 撞单再叠：罚量倍增（500→1000ms）——下次窗口更长。
	okxGateResult(k, fmt.Errorf("http 429"))
	time.Sleep(600 * time.Millisecond) // 过 520ms 首罚窗但 <1000ms 次罚窗? 次罚=next(撞时+min+1000)
	err = okxGateWait(ctx, k)
	if err == nil {
		t.Fatal("次罚窗内仍应 skip")
	}
}

func TestOkxGate_SuccessDecaysPenalty(t *testing.T) {
	resetOkxGate(20*time.Millisecond, 400*time.Millisecond, 2*time.Second)
	ctx := context.Background()
	k := "k|/p"
	okxGateWait(ctx, k)
	okxGateResult(k, fmt.Errorf("http 429")) // penalty=400
	time.Sleep(500 * time.Millisecond)       // 出首罚窗
	okxGateWait(ctx, k)
	okxGateResult(k, nil) // 成功→penalty 200
	okxGate.Lock()
	p := okxGate.paths[k].penalty
	okxGate.Unlock()
	if p != 200*time.Millisecond {
		t.Fatalf("成功减半衰减: penalty=%v", p)
	}
}

func TestOkxGate_Non429NoPenalty(t *testing.T) {
	resetOkxGate(20*time.Millisecond, time.Second, 10*time.Second)
	ctx := context.Background()
	k := "k|/p"
	okxGateWait(ctx, k)
	okxGateResult(k, fmt.Errorf("http 500 venue 故障"))
	okxGate.Lock()
	p := okxGate.paths[k].penalty
	okxGate.Unlock()
	if p != 0 {
		t.Fatalf("非 429 不加罚: %v", p)
	}
	// 正常间距后照常放行（罚量 0，距 20ms 已过）。
	time.Sleep(25 * time.Millisecond)
	if err := okxGateWait(ctx, k); err != nil {
		t.Fatalf("非 429 后应放行: %v", err)
	}
}

func TestOkxGate_CtxCancel(t *testing.T) {
	resetOkxGate(200*time.Millisecond, time.Second, 10*time.Second)
	k := "k|/p"
	okxGateWait(context.Background(), k)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := okxGateWait(ctx, k); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等待应随 ctx 取消: %v", err)
	}
}
