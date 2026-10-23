// jit_idle_test.go —— perp 闲钱扫闸锚：①snapshot 缺 fail-closed 不扫②free<minFree 不扫。
package dciexec

import (
	"context"
	"testing"

	"nox/internal/store/pgstore"
)

func TestJitIdleSweep_SnapshotMissingBlocks(t *testing.T) {
	ex := &Executor{Store: &fakeStore{},
		TradingFreeFn: func(context.Context, string, string) (float64, error) { return 5000, nil },
		JitTransferFn: func(context.Context, string, string, float64, string, string) error {
			t.Fatalf("MMR 快照缺不应划转")
			return nil
		},
	}
	if err := ex.jitIdleSweepVenue(context.Background(), "binance", "perp"); err == nil {
		t.Fatalf("快照缺应 fail-closed 拒扫")
	}
}

func TestJitIdleSweep_FreeBelowMinFreeBlocks(t *testing.T) {
	eq, buf, fm := 1000.0, 10.0, 150.0 // MMR=100 → minFree=100*2+100=300 > free 150
	ex := &Executor{Store: &fakeStore{snaps: []pgstore.VenueAccountLatest{{
		Venue: "binance", VenueType: "perp", Currency: "USDT",
		EquityRaw: &eq, BufferMultiple: &buf, FreeMarginRaw: &fm}}},
		TradingFreeFn: func(context.Context, string, string) (float64, error) { return fm, nil },
		JitTransferFn: func(context.Context, string, string, float64, string, string) error {
			t.Fatalf("free<minFree 不应划转")
			return nil
		},
	}
	if err := ex.jitIdleSweepVenue(context.Background(), "binance", "perp"); err != nil {
		t.Fatalf("低闲钱应如实静默非报错: %v", err)
	}
}
