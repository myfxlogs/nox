package exec

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nox/internal/infra/dephealth"
	"nox/internal/platform/httpx"
)

func depRowExec(key string) (dephealth.Row, bool) {
	for _, r := range dephealth.Snapshot() {
		if r.Key == key {
			return r, true
		}
	}
	return dephealth.Row{}, false
}

// TestD579_T3_CEXRestMark CEX REST 平面 dephealth 记账：wrapDepHealth 收口
// e.client 全部 Do——httptest 成/败各一断言 dephealth 快照。
func TestD579_T3_CEXRestMark(t *testing.T) {
	key := "t3gate:perp:rest"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	cli := wrapDepHealth("t3gate", "perp", httpx.Client())
	resp, err := cli.Get(srv.URL)
	if err != nil {
		t.Fatalf("httptest 应成功: %v", err)
	}
	resp.Body.Close()
	r, ok := depRowExec(key)
	if !ok || r.State != dephealth.StateHealthy {
		t.Fatalf("成功调用应标 healthy: %+v ok=%v", r, ok)
	}

	// 失败面：不可达地址→RT err→记 fail（suspect）。
	if _, err := cli.Get("http://127.0.0.1:1/"); !errors.Is(err, err) || err == nil {
		t.Fatal("不可达应 err")
	}
	r, ok = depRowExec(key)
	if !ok || r.ConsecFails != 1 || r.State != dephealth.StateSuspect {
		t.Fatalf("失败应记 suspect×1: %+v", r)
	}
}

// fakeRT 罐装 err 的 RoundTripper（T7 锚）。
type fakeRT struct{ err error }

func (f fakeRT) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// TestD579_T7_CancelNotCounted R3 F-3b：context.Canceled=本侧主动撤不记账；
// DeadlineExceeded=真上游慢保留计数。
func TestD579_T7_CancelNotCounted(t *testing.T) {
	key := fmt.Sprintf("t7-%d:perp:rest", time.Now().UnixNano()) // -count=N 复跑键隔离
	rt := depRoundTripper{key: key, rt: fakeRT{err: context.Canceled}}
	req, _ := http.NewRequest(http.MethodGet, "http://x/", nil)
	if _, err := rt.RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("err 应原样透传: %v", err)
	}
	if _, ok := depRowExec(key); ok {
		t.Fatalf("canceled 不应记账")
	}
	rt.rt = fakeRT{err: context.DeadlineExceeded}
	if _, err := rt.RoundTrip(req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err 应原样透传: %v", err)
	}
	r, ok := depRowExec(key)
	if !ok || r.ConsecFails != 1 {
		t.Fatalf("deadline 应记 fail: %+v", r)
	}
}
