// streambal_test.go —— D-523续①：执行径流缓存化石遮蔽防线锚。
// 机检口径：缓存 USDT=0 化石+快照/REST=1088 → exec 读径取真值。
package dciexec

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nox/internal/data/bus"
)

// withSpotCache 测试具：临换全局 SpotAssets（t.Cleanup 恢复）。
func withSpotCache(t *testing.T, fn func()) {
	old := bus.SpotAssets
	bus.SpotAssets = bus.NewAssetLedger()
	t.Cleanup(func() { bus.SpotAssets = old })
	fn()
}

func TestStreamBalFossilFallback(t *testing.T) {
	restCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		restCalls++
		fmt.Fprint(w, `{"balances":[{"asset":"USDT","free":"1088.80","locked":"0"}]}`)
	}))
	defer srv.Close()

	e := &Executor{
		Store: newFake(), APIBase: srv.URL, BnKey: "k", BnSec: "s",
		Client: srv.Client(), Now: time.Now,
		StreamFn: func() bool { return true },
	}
	st := e.Store.(*fakeStore)
	ctx := context.Background()

	withSpotCache(t, func() {
		// ①化石零值+快照有钱 → REST 真值
		bus.SpotAssets.Put("binance:spot", "USDT", 0, 0, time.Now())
		st.snapFree = map[string]float64{"USDT": 1088.80}
		if v, err := e.freeBalance(ctx, "USDT"); err != nil || v != 1088.80 {
			t.Fatalf("化石遮蔽未解: v=%v err=%v", v, err)
		}
		if restCalls != 1 {
			t.Fatalf("REST 兜底次数=%d", restCalls)
		}

		// ②行龄超阈 → REST
		bus.SpotAssets.Put("binance:spot", "USDT", 0, 0, time.Now().Add(-2*time.Hour))
		st.snapFree = nil // 快照缺行也不挡行龄判据
		if v, err := e.freeBalance(ctx, "USDT"); err != nil || v != 1088.80 {
			t.Fatalf("行龄化石未兜底: v=%v err=%v", v, err)
		}
		if restCalls != 2 {
			t.Fatalf("REST 兜底次数=%d", restCalls)
		}

		// ③真零（快照零）→ 零如实回，零 REST 打面
		bus.SpotAssets.Put("binance:spot", "USDT", 0, 0, time.Now())
		st.snapFree = map[string]float64{"USDT": 0}
		if v, err := e.freeBalance(ctx, "USDT"); err != nil || v != 0 {
			t.Fatalf("真零误兜底: v=%v err=%v", v, err)
		}
		if restCalls != 2 {
			t.Fatalf("真零触发 REST=%d", restCalls)
		}

		// ④活值直回（流主源语义不退）
		bus.SpotAssets.Put("binance:spot", "USDT", 500, 0, time.Now())
		if v, err := e.freeBalance(ctx, "USDT"); err != nil || v != 500 {
			t.Fatalf("活值未直回: v=%v err=%v", v, err)
		}
		if restCalls != 2 {
			t.Fatalf("活值触发 REST=%d", restCalls)
		}
	})
}

// TestStreamBalFossilFailClosed REST 兜失败 → err 如实拒（fail-closed）。
func TestStreamBalFossilFailClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	e := &Executor{
		Store: newFake(), APIBase: srv.URL, BnKey: "k", BnSec: "s",
		Client: srv.Client(), Now: time.Now,
		StreamFn: func() bool { return true },
	}
	e.Store.(*fakeStore).snapFree = map[string]float64{"USDT": 999}
	withSpotCache(t, func() {
		bus.SpotAssets.Put("binance:spot", "USDT", 0, 0, time.Now())
		if v, err := e.freeBalance(context.Background(), "USDT"); err == nil || v != 0 {
			t.Fatalf("REST 败未 fail-closed: v=%v err=%v", v, err)
		}
	})
}
