package autotrade

// D-560 S3 T6 熔断锚：同签名三连败起窗、窗内零执行+每窗 1 行 PAUSED、
// 窗到探测成败两路、异签名隔离、funding 暂停态+2h 兜底。真库（ARB_TEST_PG_DSN）。

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/errclass"
	"arb/internal/store/pgstore"
)

func d560GateFixture(t *testing.T) *Gate {
	t.Helper()
	s := setupGateAlertStore(t) // autotrade_state=RUNNING + vA/vB demo 账户
	return NewGate(s, Config{MaxDailyLoss: 1000, MaxPositions: 5, LiveTrade: false}, nil)
}

func d560Intent(dk string, execFn func(context.Context) (string, string, int64)) *TradeIntent {
	return &TradeIntent{
		Strategy: "XASSET_CARRY", Canonical: "XAUUSDT",
		VenueBuy: "vA:mt5", VenueSell: "vB:mt5",
		Qty: 0.01, NetBps: 10, DedupKey: dk, Execute: execFn,
	}
}

func d560PausedCount(t *testing.T, s *pgstore.Store) int {
	t.Helper()
	var n int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT COUNT(*) FROM autotrade_logs WHERE status='PAUSED'`).Scan(&n); err != nil {
		t.Fatalf("count paused: %v", err)
	}
	return n
}

const d560StaleMsg = "quote stale or net benefit degraded beyond threshold"
const d560FundMsg = "gate margin insufficient: need 830.00 > avail 1000.00 × 0.80 (lev 5 mark 4350.12)"

// 同签名×3 起窗；窗内 Submit 不触执行且只落 1 行 PAUSED；探测成功复位。
func TestD560_BreakerTripAndProbe(t *testing.T) {
	g := d560GateFixture(t)
	ctx := context.Background()
	dk := "XASSET_CARRY:XAUUSDT:vA:mt5:vB:mt5"
	var calls atomic.Int32
	exec := func(context.Context) (string, string, int64) {
		calls.Add(1)
		return "FAILED", d560StaleMsg, 0
	}
	for i := 0; i < 3; i++ {
		if r := g.Submit(ctx, d560Intent(dk, exec)); r.Status != "FAILED" {
			t.Fatalf("第%d次应 FAILED, got %s", i+1, r.Status)
		}
	}
	// 三连败后窗激活：再 Submit 两次——零新执行、仅 1 行 PAUSED
	for i := 0; i < 2; i++ {
		r := g.Submit(ctx, d560Intent(dk, exec))
		if r.Status != "REJECTED" || !strings.HasPrefix(r.Reason, "breaker ") {
			t.Fatalf("窗内应 breaker 拒, got %s %s", r.Status, r.Reason)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("窗内禁执行: calls=%d want 3", got)
	}
	if n := d560PausedCount(t, g.store); n != 1 {
		t.Fatalf("每窗只 1 行 PAUSED, got %d", n)
	}
	// 窗到 → 放行探测，成功=复位
	g.breakerMu.Lock()
	g.breaker[dk+"|"+errclass.RejectVariant].ResumeAt = time.Now().Add(-time.Second)
	g.breakerMu.Unlock()
	ok := func(context.Context) (string, string, int64) { calls.Add(1); return "EXECUTED", "", 7 }
	if r := g.Submit(ctx, d560Intent(dk, ok)); r.Status != "EXECUTED" {
		t.Fatalf("探测应放行执行, got %s", r.Status)
	}
	if len(g.BreakerSnapshot()) != 0 {
		t.Fatalf("探测成功应清态, snap=%v", g.BreakerSnapshot())
	}
}

// 探测失败 → 新窗重新计时（再写 1 行 PAUSED，仍每窗 1 行）。
func TestD560_BreakerProbeFailRearms(t *testing.T) {
	g := d560GateFixture(t)
	ctx := context.Background()
	dk := "XASSET_CARRY:XAUUSDT:vA:mt5:vB:mt5"
	exec := func(context.Context) (string, string, int64) { return "FAILED", d560StaleMsg, 0 }
	for i := 0; i < 3; i++ {
		g.Submit(ctx, d560Intent(dk, exec))
	}
	g.Submit(ctx, d560Intent(dk, exec)) // 窗内首跳 → PAUSED#1
	// 窗到 → 探测败 → 新窗
	g.breakerMu.Lock()
	g.breaker[dk+"|"+errclass.RejectVariant].ResumeAt = time.Now().Add(-time.Second)
	g.breakerMu.Unlock()
	if r := g.Submit(ctx, d560Intent(dk, exec)); r.Status != "FAILED" {
		t.Fatalf("探测应真执行, got %s", r.Status)
	}
	if r := g.Submit(ctx, d560Intent(dk, exec)); r.Status != "REJECTED" {
		t.Fatalf("新窗应再拒, got %s", r.Status)
	}
	if n := d560PausedCount(t, g.store); n != 2 {
		t.Fatalf("两窗各 1 行 PAUSED, got %d", n)
	}
}

// 异签名打断连续计数（同 dedupKey 不同 class 不算连击）。
func TestD560_BreakerSignatureIsolation(t *testing.T) {
	g := d560GateFixture(t)
	ctx := context.Background()
	dk := "XASSET_CARRY:XAUUSDT:vA:mt5:vB:mt5"
	stale := func(context.Context) (string, string, int64) { return "FAILED", d560StaleMsg, 0 }
	fund := func(context.Context) (string, string, int64) { return "FAILED", d560FundMsg, 0 }
	g.Submit(ctx, d560Intent(dk, stale))
	g.Submit(ctx, d560Intent(dk, stale))
	g.Submit(ctx, d560Intent(dk, fund)) // 打断 variant 连击
	g.Submit(ctx, d560Intent(dk, stale))
	if len(g.BreakerSnapshot()) != 0 {
		t.Fatalf("异签名打断后不应熔断, snap=%v", g.BreakerSnapshot())
	}
}

// funding 类三连败 → Paused=true + ResumeAt≈+2h 兜底。
func TestD560_BreakerFundingPaused(t *testing.T) {
	g := d560GateFixture(t)
	ctx := context.Background()
	dk := "XASSET_CARRY:XAUUSDT:vA:mt5:vB:mt5"
	fund := func(context.Context) (string, string, int64) { return "FAILED", d560FundMsg, 0 }
	for i := 0; i < 3; i++ {
		g.Submit(ctx, d560Intent(dk, fund))
	}
	snap := g.BreakerSnapshot()
	info, ok := snap[dk+"|"+errclass.RejectFunding]
	if !ok || !info.Paused {
		t.Fatalf("funding 应 paused, snap=%v", snap)
	}
	if d := time.Until(info.ResumeAt); d < 110*time.Minute || d > 125*time.Minute {
		t.Fatalf("funding 兜底应 ~2h, got %s", d)
	}
	// ResumeBreaker 清态（ResumeXAssetCombo 行为源）
	if n := g.ResumeBreaker(dk); n != 1 || len(g.BreakerSnapshot()) != 0 {
		t.Fatalf("resume 应清 1 条, n=%d snap=%v", n, g.BreakerSnapshot())
	}
}
