// d591_ban_backoff_test.go D-591 S1：listenKey 封禁窗感知退避。
// S0 实证（2026-09-30）：cex-proxy flap×5s 盲重连每轮 POST listenKey=封禁诱因，
// 且封禁窗内盲锤续期封禁（04:10:50→04:14:51）——退避至到期+抖动，窗内零 REST。
package exchange

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newBanTestWS(base string) *BinanceUserWS {
	return NewBinanceUserWS("K", "S", base, "perp", "binance:perp", false, nil, nil,
		NewOrderEventCache(), NewPositionEventCache(), BinanceUserHooks{})
}

// banServer 恒回 -1003 封禁体（until=相对 now 的秒数），计数 POST /v1/listenKey。
func banServer(posts *int64, banIn time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(posts, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(w, `{"code":-1003,"msg":"Way too many requests; IP banned until %d. Please use the websocket for live updates to avoid bans."}`,
			time.Now().Add(banIn).UnixMilli())
	}))
}

// TestD591_ListenKeyBanBackoffZeroPOST 封禁窗内零重试 POST：ban 60s、跑 7s——
// 退避生效=恰好 1 次；旧码 5s 盲锤在 7s 窗内发 2 次（RED）。
func TestD591_ListenKeyBanBackoffZeroPOST(t *testing.T) {
	var posts int64
	rest := banServer(&posts, 60*time.Second)
	defer rest.Close()
	old := banJitter
	banJitter = time.Second // 抖动界入 1s：退避到期=until+0..1s，远超测试窗
	defer func() { banJitter = old }()

	b := newBanTestWS(rest.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	b.Run(ctx)
	if n := atomic.LoadInt64(&posts); n != 1 {
		t.Fatalf("封禁窗内 POST want 1, got %d（窗内零发射未生效）", n)
	}
}

// TestD591_ListenKeyBanRecovery 封禁到期即恢复尝试（抖动界入 0）：ban 2s、跑 7s——
// 节拍 0s/2s/4s/6s≥3 次；旧码 5s 盲锤在 7s 窗内仅 0s/5s=2 次（RED，恢复慢于到期节拍）。
func TestD591_ListenKeyBanRecovery(t *testing.T) {
	var posts int64
	rest := banServer(&posts, 2*time.Second)
	defer rest.Close()
	old := banJitter
	banJitter = 0
	defer func() { banJitter = old }()

	b := newBanTestWS(rest.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	b.Run(ctx)
	if n := atomic.LoadInt64(&posts); n < 3 {
		t.Fatalf("到期后恢复节拍 want ≥3 POST（0/2/4/6s）, got %d", n)
	}
}
