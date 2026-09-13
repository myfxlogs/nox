// D-293 S2 测试：account_collector onAccountFetched hook panic recover。
// 验证 hook panic 不拖垮采集主循环（Poll 正常返回）。
package trx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAccountCollector_HookPanicRecover hook panic → Poll 不崩溃，正常返回。
// D-293 S2 红绿锚：hook panic 不得拖垮采集主循环。
func TestAccountCollector_HookPanicRecover(t *testing.T) {
	st := newTestStore(t) // DSN-gated（ARB_TEST_PG_DSN）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/wallet/getaccount":
			_, _ = w.Write([]byte(mockGetAccountJSON))
		case "/wallet/getaccountresource":
			_, _ = w.Write([]byte(mockGetResourceJSON))
		default:
			// tronscan /api/account (rewardNum)
			_, _ = w.Write([]byte(`{"rewardNum":0}`))
		}
	}))
	defer srv.Close()

	c := NewAccountCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL, srv.URL, "")
	panicked := false
	c.SetOnAccountFetched(func(ctx context.Context, rewardNumSun int64) {
		panicked = true
		panic("intentional hook panic for test")
	})

	facts, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll should not return error when hook panics: %v", err)
	}
	if facts != nil {
		t.Errorf("Poll should return nil facts, got %v", facts)
	}
	if !panicked {
		t.Error("hook should have been called (and panicked)")
	}
}

// TestAccountCollector_HookCalled hook 正常调用 → 传入 rewardNumSun。
func TestAccountCollector_HookCalled(t *testing.T) {
	st := newTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/wallet/getaccount":
			_, _ = w.Write([]byte(mockGetAccountJSON))
		case "/wallet/getaccountresource":
			_, _ = w.Write([]byte(mockGetResourceJSON))
		default:
			_, _ = w.Write([]byte(`{"rewardNum":15000000}`)) // 15 TRX
		}
	}))
	defer srv.Close()

	c := NewAccountCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL, srv.URL, "")
	var gotReward int64
	c.SetOnAccountFetched(func(ctx context.Context, rewardNumSun int64) {
		gotReward = rewardNumSun
	})

	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if gotReward != 15000000 {
		t.Errorf("hook rewardNumSun = %d, want 15000000", gotReward)
	}
}

// TestAccountCollector_TrionscanFailCarryPrevReward D-295 S1：tronscan 失败回退上一快照 rewardNum（禁写 0）。
// 红绿锚：旧代码失败轮写 0 → 新代码回退上一快照 15000000。
func TestAccountCollector_TrionscanFailCarryPrevReward(t *testing.T) {
	st := newTestStore(t)
	ok := true // server flag 控制 /api/account 首轮成功、次轮 500
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/wallet/getaccount":
			_, _ = w.Write([]byte(mockGetAccountJSON))
		case "/wallet/getaccountresource":
			_, _ = w.Write([]byte(mockGetResourceJSON))
		default:
			// tronscan /api/account
			if ok {
				_, _ = w.Write([]byte(`{"rewardNum":15000000}`))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"tronscan down"}`))
		}
	}))
	defer srv.Close()

	c := NewAccountCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL, srv.URL, "")

	// 第一次 Poll：tronscan 成功，rewardNum=15000000。
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("first Poll: %v", err)
	}
	last, err := st.LastTRXAccount(context.Background())
	if err != nil {
		t.Fatalf("LastTRXAccount after first Poll: %v", err)
	}
	if last.RewardNumSun != 15000000 {
		t.Fatalf("first Poll RewardNumSun = %d, want 15000000", last.RewardNumSun)
	}

	// 第二次 Poll：tronscan 500，应回退上一快照 15000000（不是 0）。
	ok = false
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	last, err = st.LastTRXAccount(context.Background())
	if err != nil {
		t.Fatalf("LastTRXAccount after second Poll: %v", err)
	}
	if last.RewardNumSun != 15000000 {
		t.Errorf("second Poll RewardNumSun = %d, want 15000000 (carry prev, not 0)", last.RewardNumSun)
	}
}
