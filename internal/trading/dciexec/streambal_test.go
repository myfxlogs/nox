// streambal_test.go —— D-523续①化石防线锚（D-604 R2 重写：LatestSnapshotFree
// 对拍层删——化石判据=行龄超阈单判据，行龄内零值=venue 真值如实上抛）。
// 机检口径：行龄超阈零值→REST 兜底真值；行龄内零值→零如实回（真零语义）；REST 败 fail-closed。
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

	e := &Executor{ModeGate: liveModeGate, SigmaFn: testSigma,
		Store: newFake(), APIBase: srv.URL, BnKey: "k", BnSec: "s",
		Client: srv.Client(), Now: time.Now,
		StreamFn: func() bool { return true },
	}
	ctx := context.Background()

	withSpotCache(t, func() {
		// ①行龄超阈零值（化石）→ REST 兜底真值
		bus.SpotAssets.Put("binance:spot", "USDT", 0, 0, time.Now().Add(-2*time.Hour))
		if v, err := e.freeBalance(ctx, "USDT"); err != nil || v != 1088.80 {
			t.Fatalf("行龄化石未兜底: v=%v err=%v", v, err)
		}
		if restCalls != 1 {
			t.Fatalf("REST 兜底次数=%d", restCalls)
		}

		// ②行龄内零值=venue 真值如实回（D-604 R2：对拍层删——零不触发 REST）
		bus.SpotAssets.Put("binance:spot", "USDT", 0, 0, time.Now())
		if v, err := e.freeBalance(ctx, "USDT"); err != nil || v != 0 {
			t.Fatalf("行龄内真零须如实回: v=%v err=%v", v, err)
		}
		if restCalls != 1 {
			t.Fatalf("真零误触发 REST=%d", restCalls)
		}

		// ③活值直回（流主源语义不退）
		bus.SpotAssets.Put("binance:spot", "USDT", 500, 0, time.Now())
		if v, err := e.freeBalance(ctx, "USDT"); err != nil || v != 500 {
			t.Fatalf("活值未直回: v=%v err=%v", v, err)
		}
		if restCalls != 1 {
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
	e := &Executor{ModeGate: liveModeGate, SigmaFn: testSigma,
		Store: newFake(), APIBase: srv.URL, BnKey: "k", BnSec: "s",
		Client: srv.Client(), Now: time.Now,
		StreamFn: func() bool { return true },
	}
	withSpotCache(t, func() {
		// 行龄超阈（化石）→REST 兜底；REST 败→fail-closed err。
		bus.SpotAssets.Put("binance:spot", "USDT", 0, 0, time.Now().Add(-2*time.Hour))
		if v, err := e.freeBalance(context.Background(), "USDT"); err == nil || v != 0 {
			t.Fatalf("REST 败未 fail-closed: v=%v err=%v", v, err)
		}
	})
}
