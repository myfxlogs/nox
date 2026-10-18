// D-234 帧钩子测试：type7/type6/type5 帧 → SetFrameHook 回调按帧种类触发（先红后绿）。
package cmc

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestFrameHook_NilSafe D-234：frameHook 未接线（nil）→ fireFrameHook 不 panic。
func TestFrameHook_NilSafe(t *testing.T) {
	w := &WsClient{}
	// nil hook → 不 panic，静默返回
	w.fireFrameHook(FramePrice)
	w.fireFrameHook(FrameAccount)
	w.fireFrameHook(FramePosition)
}

// TestFrameHook_RecoverIsolatesPanic D-234：hook panic 不传播（通知失败不影响落库，硬约束⑦）。
func TestFrameHook_RecoverIsolatesPanic(t *testing.T) {
	w := &WsClient{}
	w.SetFrameHook(func(kind FrameKind) { panic("boom") })
	// panic 被 recover，不传播
	w.fireFrameHook(FramePrice)
}

// TestFrameHook_Type7FiresPrice D-234：type7 首帧种子 → frameHook 收到 FramePrice。
// 走真实 WS 测试服务器路径（handleType7 → extractPriceEvent → fireFrameHook）。
func TestFrameHook_Type7FiresPrice(t *testing.T) {
	st := newTestStore(t)
	ts := newWsTestServer(t, mockPositionsJSON)
	defer ts.close()
	auth := &mockAuth{jwt: "fake.jwt.sig"}
	ws := NewWsClient(auth, 38941616, st, ts.srv.URL, ts.srv.URL)

	var mu sync.Mutex
	var gotKinds []FrameKind
	ws.SetFrameHook(func(kind FrameKind) {
		mu.Lock()
		gotKinds = append(gotKinds, kind)
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ws.Run(ctx)
	if !ts.waitSubscribed(10 * time.Second) {
		t.Fatal("timeout waiting for subscribe")
	}
	// 推 type7 首帧全量快照
	ts.sendEvent(0, wsType7SnapshotEvent)
	time.Sleep(1 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	foundPrice := false
	for _, k := range gotKinds {
		if k == FramePrice {
			foundPrice = true
		}
	}
	if !foundPrice {
		t.Fatalf("frameHook did not receive FramePrice; got kinds %v", gotKinds)
	}
}

// TestFrameHook_Type6FiresAccount D-234：type6 账户摘要帧 → frameHook 收到 FrameAccount。
func TestFrameHook_Type6FiresAccount(t *testing.T) {
	st := newTestStore(t)
	ts := newWsTestServer(t, mockPositionsJSON)
	defer ts.close()
	auth := &mockAuth{jwt: "fake.jwt.sig"}
	ws := NewWsClient(auth, 38941616, st, ts.srv.URL, ts.srv.URL)

	var gotAccount atomic.Int32
	ws.SetFrameHook(func(kind FrameKind) {
		if kind == FrameAccount {
			gotAccount.Add(1)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ws.Run(ctx)
	if !ts.waitSubscribed(10 * time.Second) {
		t.Fatal("timeout waiting for subscribe")
	}
	// 推 type6 账户摘要帧
	ts.sendEvent(0, wsType6Event)
	time.Sleep(1 * time.Second)

	if gotAccount.Load() == 0 {
		t.Fatal("frameHook did not receive FrameAccount")
	}
}
