// D-117 锚：账户投票读面 tronscan 主读 + gRPC 兜底 + 失败告警 episode 去重。
package trxvote

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A1：tronscan /api/vote 正常响应 → Votes 映射 + TronPower=voterAvailableVotes。
func TestD117_TronscanAccountFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/vote" || r.URL.Query().Get("voter") == "" {
			t.Errorf("unexpected path %s query %s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"1","totalVotes":107332,"data":[
			{"candidateAddress":"TSR1","votes":35777,"voterAvailableVotes":107332},
			{"candidateAddress":"TSR2","votes":35777,"voterAvailableVotes":107332},
			{"candidateAddress":"TSR3","votes":35778,"voterAvailableVotes":107332}
		]}`))
	}))
	defer srv.Close()
	old := httpClient
	SetHTTPClient(srv.Client())
	defer SetHTTPClient(old)

	f := NewTronscanAccountFetcher(srv.URL)
	d, err := f.GetAccountDetail(context.Background(), "TAddr")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(d.Votes) != 3 || d.Votes["TSR3"] != 35778 {
		t.Fatalf("votes=%v", d.Votes)
	}
	if d.TronPower != 107332 {
		t.Fatalf("tp=%d", d.TronPower)
	}
}

// A2：voterAvailableVotes 缺省 → TronPower 回退 Σvotes。
func TestD117_TronscanAccountFallbackTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalVotes":10,"data":[{"candidateAddress":"TSR1","votes":10}]}`))
	}))
	defer srv.Close()
	old := httpClient
	SetHTTPClient(srv.Client())
	defer SetHTTPClient(old)

	d, err := NewTronscanAccountFetcher(srv.URL).GetAccountDetail(context.Background(), "T")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if d.TronPower != 10 {
		t.Fatalf("tp=%d want 10 (Σvotes)", d.TronPower)
	}
}

// A3：主读失败走兜底；双失败才报 err。
type stubFetcher struct {
	d   *AccountDetail
	err error
}

func (s *stubFetcher) GetAccountDetail(context.Context, string) (*AccountDetail, error) {
	return s.d, s.err
}

func TestD117_FallbackChain(t *testing.T) {
	want := &AccountDetail{Votes: map[string]int64{"TSR1": 5}, TronPower: 5}
	f := NewFallbackAccountFetcher(&stubFetcher{err: errors.New("429")}, &stubFetcher{d: want})
	d, err := f.GetAccountDetail(context.Background(), "T")
	if err != nil || d != want {
		t.Fatalf("d=%v err=%v", d, err)
	}

	f = NewFallbackAccountFetcher(&stubFetcher{err: errors.New("429")}, &stubFetcher{err: errors.New("backup down")})
	if _, err := f.GetAccountDetail(context.Background(), "T"); err == nil {
		t.Fatal("want err when both fail")
	}

	// backup=nil → 纯主读
	f = NewFallbackAccountFetcher(&stubFetcher{d: want}, nil)
	if _, ok := f.(*stubFetcher); !ok {
		t.Fatalf("nil backup should return primary, got %T", f)
	}
}

// A4：失败告警 episode 去重——连续失败只警一次，恢复边沿报一条，再败再警。
func TestD117_AlertEpisodeDedup(t *testing.T) {
	store := &fakeStore{}
	var alerts []string
	sink := func(level, msg string) { alerts = append(alerts, msg) }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"address":"TSR1","name":"sr1","annualizedRate":"5.0","brokerage":80,"realTimeVotes":1000,"votes":1000,"producer":true}]`))
	}))
	defer srv.Close()
	old := httpClient
	SetHTTPClient(srv.Client())
	defer SetHTTPClient(old)

	sched, exec := testScheduler(t, store, &mockAccountFetcher{err: errors.New("429 boom")}, &testExecutorConn{})
	exec.alertSink = sink
	sched.tronscanBase = srv.URL

	// 三次连续失败 → 仅一条 [投票账户查询失败]
	sched.doRebalance(context.Background())
	sched.doRebalance(context.Background())
	sched.doRebalance(context.Background())
	if n := countSub(alerts, "投票账户查询失败"); n != 1 {
		t.Fatalf("fail alerts=%d want 1 (episode dedup): %v", n, alerts)
	}

	// 恢复 → 一条恢复边沿
	sched.account = &mockAccountFetcher{acc: &AccountDetail{Votes: map[string]int64{"TSR1": 5}, TronPower: 5}}
	sched.doRebalance(context.Background())
	if n := countSub(alerts, "投票账户查询恢复"); n != 1 {
		t.Fatalf("recovery alerts=%d want 1: %v", n, alerts)
	}

	// 再失败 → 新一轮 episode 再警一条
	sched.account = &mockAccountFetcher{err: errors.New("429 again")}
	sched.doRebalance(context.Background())
	if n := countSub(alerts, "投票账户查询失败"); n != 2 {
		t.Fatalf("post-recovery fail alerts=%d want 2: %v", n, alerts)
	}
}

func countSub(ss []string, sub string) int {
	n := 0
	for _, s := range ss {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}
