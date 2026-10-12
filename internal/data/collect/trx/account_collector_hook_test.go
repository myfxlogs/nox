// 测试：account_collector onAccountFetched hook panic recover。
// 验证 hook panic 不拖垮采集主循环（Poll 正常返回）。
package trx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestAccountCollector_HookPanicRecover hook panic → Poll 不崩溃，正常返回。
// 红绿锚：hook panic 不得拖垮采集主循环。
func TestAccountCollector_HookPanicRecover(t *testing.T) {
	st := newTestStore(t) // DSN-gated（NOX_TEST_PG_DSN）
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

	c := NewAccountCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL, srv.URL, func() string { return "" })
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

	c := NewAccountCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL, srv.URL, func() string { return "" })
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

	c := NewAccountCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL, srv.URL, func() string { return "" })

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

// TestAccountCollector_RewardChangeFiresOnChange D-295 S6：rewardNum 变化触发 onChange（解冻页面刷新）。
// 红绿锚：旧指纹不含 rewardNum → 第二轮不触发；新指纹含 rewardNum → 第二轮触发。
func TestAccountCollector_RewardChangeFiresOnChange(t *testing.T) {
	st := newTestStore(t)
	reward := int64(15000000) // server flag 切 rewardNum
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/wallet/getaccount":
			_, _ = w.Write([]byte(mockGetAccountJSON))
		case "/wallet/getaccountresource":
			_, _ = w.Write([]byte(mockGetResourceJSON))
		default:
			_, _ = w.Write([]byte(`{"rewardNum":` + strconv.FormatInt(reward, 10) + `}`))
		}
	}))
	defer srv.Close()

	c := NewAccountCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL, srv.URL, func() string { return "" })
	fired := 0
	c.SetOnChange(func() { fired++ })

	// 第一轮 Poll：rewardNum=15000000，首轮不触发 onChange（firstPoll 语义）。
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("first Poll: %v", err)
	}
	if fired != 0 {
		t.Errorf("first Poll fired=%d, want 0 (firstPoll 不触发)", fired)
	}

	// 第二轮 Poll：rewardNum=20000000，指纹变化 → onChange 触发。
	reward = 20000000
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	if fired != 1 {
		t.Errorf("second Poll fired=%d, want 1 (rewardNum 变化触发 onChange；D-295 S6)", fired)
	}
}

// TestAccountCollector_RewardDeltaBoundary D-137①：rewardNum 差分=维护期边界探测器。
// 非零 delta → onRewardDelta 触发；零 delta（含首轮/持平/D-295 回退前值）→ 不触发。
func TestAccountCollector_RewardDeltaBoundary(t *testing.T) {
	st := newTestStore(t)
	reward := int64(15000000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/wallet/getaccount":
			_, _ = w.Write([]byte(mockGetAccountJSON))
		case "/wallet/getaccountresource":
			_, _ = w.Write([]byte(mockGetResourceJSON))
		default:
			_, _ = w.Write([]byte(`{"rewardNum":` + strconv.FormatInt(reward, 10) + `}`))
		}
	}))
	defer srv.Close()

	c := NewAccountCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL, srv.URL, func() string { return "" })
	type evt struct{ reward, delta int64 }
	var fired []evt
	c.SetOnRewardDelta(func(_ context.Context, rewardNumSun, delta int64) {
		fired = append(fired, evt{rewardNumSun, delta})
	})

	poll := func() {
		if _, err := c.Poll(context.Background()); err != nil {
			t.Fatalf("Poll: %v", err)
		}
	}

	poll() // 首轮：无前值不触发
	if len(fired) != 0 {
		t.Fatalf("first Poll fired=%v, want 0（首轮前值未知）", fired)
	}

	poll() // 零 delta 不触发
	if len(fired) != 0 {
		t.Fatalf("zero delta fired=%v, want 0（边界静默窗不动作）", fired)
	}

	reward = 18000000 // 边界结算步进 → 非零 delta 触发
	poll()
	if len(fired) != 1 || fired[0].reward != 18000000 || fired[0].delta != 3000000 {
		t.Fatalf("boundary delta fired=%v, want [{18000000,+3000000}]", fired)
	}

	reward = 0 // claim 后 reward 清零 → 负 delta 亦触发（自事件边界）
	poll()
	if len(fired) != 2 || fired[1].delta != -18000000 {
		t.Fatalf("claim zero-reset fired=%v, want second evt delta=-18000000", fired)
	}
}
