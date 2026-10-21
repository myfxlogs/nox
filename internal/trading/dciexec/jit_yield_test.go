// jit_yield_test.go —— D-647 jit 收益比对闸+闲置回申锚测：
// 机会≤活期拒赎/APR 读败缺 fail-closed/机会>活期放行；armed==0 闲置全回申
// jit-idle 前缀（净额账不混）/armed>0 不扫荡闲置。
package dciexec

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// 机会≤活期：拒赎（rail 零赎回）——14.7% 案例锚（earn 20% 更优如实拒）。
func TestJitYield_LowerOppRejects(t *testing.T) {
	rail := &fakeRail{positions: []EarnPosition{{ProductID: "p", Amount: 1e4}}}
	ex, st := railFixture("gate", rail)
	st.earnAPR = 0.20
	if err := ex.JitSweep(context.Background(), "gate:perp", 100, 0.147); err == nil ||
		!strings.Contains(err.Error(), "收益比对拒赎") {
		t.Fatalf("opp<earn 未拒: %v", err)
	}
	if rail.redeemN != 0 {
		t.Fatal("拒赎仍发赎回")
	}
}

// earn 在册 APR 读败/缺 → fail-closed 不赎（比对不可行不臆放）。
func TestJitYield_EarnAPRMissingFailClosed(t *testing.T) {
	for _, seed := range []struct {
		name string
		okv  bool
		err  error
	}{
		{"APR行缺", false, nil},
		{"APR读败", false, errors.New("db boom")},
	} {
		rail := &fakeRail{positions: []EarnPosition{{ProductID: "p", Amount: 1e4}}}
		ex, st := railFixture("gate", rail)
		st.earnAPROK = seed.okv
		st.earnAPRErr = seed.err
		if err := ex.JitSweep(context.Background(), "gate:perp", 100, 0.147); err == nil {
			t.Fatalf("%s 未拒", seed.name)
		}
		if rail.redeemN != 0 {
			t.Fatalf("%s 仍发赎回", seed.name)
		}
	}
}

// 机会>活期 → 放行（闸过走原 sweep 链）。
func TestJitYield_HigherOppPasses(t *testing.T) {
	rail := &fakeRail{positions: []EarnPosition{{ProductID: "p", Amount: 1e4}}}
	ex, st := railFixture("gate", rail)
	st.earnAPR = 0.02
	ex.JitTransferFn = func(_ context.Context, _, _ string, _ float64, _, _ string) error { return nil }
	if err := ex.JitSweep(context.Background(), "gate:perp", 100, 0.147); err != nil {
		t.Fatalf("opp>earn 应放行: %v", err)
	}
	if rail.redeemN != 1 {
		t.Fatalf("赎回 %d≠1", rail.redeemN)
	}
}

// 闲置回申：armed==0+outstanding=0+free=300 → 全额回申且 io 前缀 jit-idle
// （净额账 jit-ret 零行——差分不混）。
func TestJitReturn_IdleResubscribe(t *testing.T) {
	rail := &fakeRail{products: []EarnProduct{{ProductID: "gate-uni-USDT", CanPurchase: true}}}
	ex, st := railFixture("gate", rail)
	st.armedN = 0
	st.jitOutstanding = 0
	ex.TradingFreeFn = func(_ context.Context, _, _ string) (float64, error) { return 300, nil }
	ex.JitTransferFn = func(_ context.Context, _, _ string, _ float64, _, _ string) error { return nil }
	fakeIOCaptures = nil
	if err := ex.jitReturn(context.Background(), "gate", "perp"); err != nil {
		t.Fatalf("idle 回申: %v", err)
	}
	if rail.prodSub != 1 {
		t.Fatalf("回申 %d≠1", rail.prodSub)
	}
	var retRows, idleRows int
	for _, io := range fakeIOCaptures {
		if strings.HasPrefix(io.ExternalID, "jit-ret:gate:") {
			retRows++
		}
		if strings.HasPrefix(io.ExternalID, "jit-idle:gate:") {
			idleRows++
			if io.Amount != 300 {
				t.Fatalf("idle io 额 %.2f≠300", io.Amount)
			}
		}
	}
	if retRows != 0 || idleRows != 2 {
		t.Fatalf("io 前缀错: ret=%d idle=%d（净额账须零 jit-ret 行）", retRows, idleRows)
	}
}

// armed==0 且有 outstanding：净额段 jit-ret+闲置段 jit-idle 分账。
func TestJitReturn_IdlePlusOutstanding(t *testing.T) {
	rail := &fakeRail{products: []EarnProduct{{ProductID: "gate-uni-USDT", CanPurchase: true}}}
	ex, st := railFixture("gate", rail)
	st.armedN = 0
	st.jitOutstanding = 200
	ex.TradingFreeFn = func(_ context.Context, _, _ string) (float64, error) { return 500, nil }
	ex.JitTransferFn = func(_ context.Context, _, _ string, _ float64, _, _ string) error { return nil }
	fakeIOCaptures = nil
	if err := ex.jitReturn(context.Background(), "gate", "perp"); err != nil {
		t.Fatalf("ret: %v", err)
	}
	var retSum, idleSum float64
	for _, io := range fakeIOCaptures {
		if strings.HasPrefix(io.ExternalID, "jit-ret:gate:") {
			retSum += io.Amount
		}
		if strings.HasPrefix(io.ExternalID, "jit-idle:gate:") {
			idleSum += io.Amount
		}
	}
	if retSum != 400 || idleSum != 600 { // 每段 trx+sub 两行：200×2 / 300×2
		t.Fatalf("分账错: ret=%.0f idle=%.0f", retSum, idleSum)
	}
}

// armed>0：闲置不扫荡——outstanding=0 直接 nil（tradingFree 零调）。
func TestJitReturn_ArmedSkipsIdle(t *testing.T) {
	ex, st := railFixture("gate", &fakeRail{})
	st.armedN = 1
	st.jitOutstanding = 0
	freeCalls := 0
	ex.TradingFreeFn = func(_ context.Context, _, _ string) (float64, error) { freeCalls++; return 999, nil }
	if err := ex.jitReturn(context.Background(), "gate", "perp"); err != nil {
		t.Fatalf("armed 净零应静默: %v", err)
	}
	if freeCalls != 0 {
		t.Fatalf("armed>0 仍读 free（idle 面被误触）")
	}
}
