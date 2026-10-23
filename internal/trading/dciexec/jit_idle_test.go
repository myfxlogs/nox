// jit_idle_test.go —— perp 闲钱扫闸锚：①快照缺 fail-closed②空仓 MMR=0 地板即扫③有仓 used 保守源不扫。
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
			t.Fatalf("快照缺不应划转")
			return nil
		},
	}
	if err := ex.jitIdleSweepVenue(context.Background(), "binance", "perp"); err == nil {
		t.Fatalf("快照缺应 fail-closed 拒扫")
	}
}

// 空仓（equity=free）：MMR=0→minFree=地板 $100——free 150 仍有超额但
// rail/产品面断供如实拒（闸后径断供如实报非划转——零金动锚）。
func TestJitIdleSweep_EmptyBookMMRZero(t *testing.T) {
	eq, fm := 150.0, 150.0
	var moved bool
	ex := &Executor{Store: &fakeStore{snaps: []pgstore.VenueAccountLatest{{
		Venue: "binance", VenueType: "perp", Currency: "USDT",
		EquityRaw: &eq, FreeMarginRaw: &fm}}},
		TradingFreeFn: func(context.Context, string, string) (float64, error) { return fm, nil },
		JitTransferFn: func(context.Context, string, string, float64, string, string) error {
			moved = true
			return nil
		},
	}
	// rail 缺→闸过后 earn rail 缺席如实报（不划转——rail 判定先于划转）
	if err := ex.jitIdleSweepVenue(context.Background(), "binance", "perp"); err == nil {
		t.Fatalf("rail 缺应如实拒")
	}
	if moved {
		t.Fatalf("rail 缺不应划转")
	}
}

// 有仓（equity>free）：used=equity−free=MMR 保守源→minFree=used×2+100——
// 1500/1350 户（used=150→minFree=400>free 150）不扫锚。
func TestJitIdleSweep_OpenBookConservative(t *testing.T) {
	eq, fm := 1500.0, 1350.0
	ex := &Executor{Store: &fakeStore{snaps: []pgstore.VenueAccountLatest{{
		Venue: "binance", VenueType: "perp", Currency: "USDT",
		EquityRaw: &eq, FreeMarginRaw: &fm}}},
		TradingFreeFn: func(context.Context, string, string) (float64, error) { return 150, nil },
		JitTransferFn: func(context.Context, string, string, float64, string, string) error {
			t.Fatalf("有仓 used 保守源 minFree=400 不应划转")
			return nil
		},
	}
	if err := ex.jitIdleSweepVenue(context.Background(), "binance", "perp"); err != nil {
		t.Fatalf("有仓超额不足应如实静默非报错: %v", err)
	}
}

// /v2/balance 实载荷形字段名锚（返修⑨——walletBalance 键不在此端点，
// maxWithdrawAmount 权威可转额）。
func TestBinanceFapiFreeUSDT_FieldAnchor(t *testing.T) {
	v, err := binanceFapiFreeUSDT([]byte(`[
		{"asset":"USDT","balance":"1000.0200","maxWithdrawAmount":"900.5000","availableBalance":"1344.0560"},
		{"asset":"BFUSD","balance":"346.21","maxWithdrawAmount":"0"}
	]`))
	if err != nil || v != 900.5 {
		t.Fatalf("maxWithdrawAmount 字段锚: v=%v err=%v", v, err)
	}
	// 缺 maxWithdrawAmount 键→0 静默证（同病灶复现面如实 0）。
	v, _ = binanceFapiFreeUSDT([]byte(`[{"asset":"USDT","walletBalance":"1000.02"}]`))
	if v != 0 {
		t.Fatalf("walletBalance 错键应返 0（端点面锚）: %v", v)
	}
}
