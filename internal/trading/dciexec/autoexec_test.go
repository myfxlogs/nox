// autoexec_test.go —— D-260 M4.2：AutoExecLive 闸锚测。
// 非 live=零 venue 请求（fail-closed 可观察面）；live=尝试穿既有闸序。
package dciexec

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestM42_AutoExecNonLiveFailClosed 无 fin_strategy 行/非 live→零 venue 调用。
func TestM42_AutoExecNonLiveFailClosed(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	fs := newFake()
	pid := fs.addProposal("dci_sell_high_covered_call") // viable 卡默认种
	_ = pid
	e := &Executor{Store: fs, BnKey: "k", BnSec: "s", Sapi: srv.URL, APIBase: srv.URL,
		Client: srv.Client(), Now: time.Now,
		PriceFn: func(context.Context, string) (float64, error) { return 100000, nil }}
	n, err := e.AutoExecLive(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("non-live autoexec n=%d err=%v want 0", n, err)
	}
	if atomic.LoadInt64(&hits) != 0 {
		t.Fatalf("非 live 触发 %d 次 venue 调用——fail-closed 破", hits)
	}
	// demoted/retired/candidate 同拒。
	for _, st := range []string{"candidate", "paper", "graduated", "demoted", "retired"} {
		fs.strategyStates = map[string]string{"dci_sell_high_covered_call": st}
		if n, err := e.AutoExecLive(context.Background()); err != nil || n != 0 {
			t.Fatalf("state=%s autoexec n=%d want 0", st, n)
		}
	}
	if atomic.LoadInt64(&hits) != 0 {
		t.Fatalf("非 live 态集触发 venue 调用 %d 次", hits)
	}
}

// TestM42_AutoExecLiveThrough live→提案穿 GoProposal 闸序（venue 调用发生=进入执行径）。
func TestM42_AutoExecLiveThrough(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	fs := newFake()
	fs.addProposal("dci_sell_high_covered_call")
	fs.strategyStates = map[string]string{"dci_sell_high_covered_call": "live"}
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"BTC": 0.06})
	e.APIBase = srv.URL
	_, err := e.AutoExecLive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt64(&hits) == 0 {
		t.Fatal("live 态应进入执行径（venue 调用≥1）")
	}
}
