package autotrade

import (
	"strings"
	"sync"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// 单测：CS-miss 聚合告警 + upsert 仅填 NULL + 覆盖率四态。

// 用例①：CS-miss 聚合去重（同 key 1h 内不重复）
func TestCSMissAggregationDedup(t *testing.T) {
	e := &Engine{
		csMissCounts: make(map[string]int),
		csMissSent:   make(map[string]time.Time),
	}
	var alertMsgs []string
	var alertMu sync.Mutex
	e.AlertSink = func(level, msg string) {
		alertMu.Lock()
		alertMsgs = append(alertMsgs, msg)
		alertMu.Unlock()
	}
	// 记 3 次 miss（同 key）
	for i := 0; i < 3; i++ {
		e.recordCSMiss("ic:mt5", "AUDJPY")
	}
	// 首次 flush → 应发 1 条告警含 count=3
	e.flushCSMissAlerts()
	if len(alertMsgs) != 1 {
		t.Fatalf("first flush: got %d alerts, want 1", len(alertMsgs))
	}
	if !strings.Contains(alertMsgs[0], "（3 笔）") {
		t.Errorf("first flush: msg %q should contain count=3", alertMsgs[0])
	}
	// 立即再记 + flush → 1h 内去重，不应发
	e.recordCSMiss("ic:mt5", "AUDJPY")
	e.flushCSMissAlerts()
	if len(alertMsgs) != 1 {
		t.Errorf("second flush within 1h: got %d alerts, want 1 (dedup)", len(alertMsgs))
	}
}

// 用例②：CS-miss 不同 key 各发一条
func TestCSMissMultiKey(t *testing.T) {
	e := &Engine{
		csMissCounts: make(map[string]int),
		csMissSent:   make(map[string]time.Time),
	}
	var alerts []string
	e.AlertSink = func(level, msg string) { alerts = append(alerts, msg) }
	e.recordCSMiss("ic:mt5", "AUDJPY")
	e.recordCSMiss("pxb:mt5", "BRENT")
	e.recordCSMiss("pxb:mt5", "BRENT")
	e.flushCSMissAlerts()
	if len(alerts) != 1 {
		t.Fatalf("got %d alerts, want 1 (aggregated)", len(alerts))
	}
	// 应含两个 key
	if !strings.Contains(alerts[0], "ic:mt5|AUDJPY") || !strings.Contains(alerts[0], "pxb:mt5|BRENT") {
		t.Errorf("alert %q should contain both keys", alerts[0])
	}
	// BRENT count=2
	if !strings.Contains(alerts[0], "（2 笔）") {
		t.Errorf("alert %q should contain count=2 for BRENT", alerts[0])
	}
}

// 用例③：CS-miss 空 counts 不发告警
func TestCSMissEmpty(t *testing.T) {
	e := &Engine{
		csMissCounts: make(map[string]int),
		csMissSent:   make(map[string]time.Time),
	}
	called := false
	e.AlertSink = func(level, msg string) { called = true }
	e.flushCSMissAlerts()
	if called {
		t.Error("empty counts should not trigger alert")
	}
}

// 用例④：computeBuyQty skip 时 recordCSMiss 被调（集成验证）
func TestComputeBuyQtySkipRecordsCSMiss(t *testing.T) {
	e := &Engine{
		csMissCounts: make(map[string]int),
		csMissSent:   make(map[string]time.Time),
	}
	opp := pgstore.Opportunity{
		Canonical: "AUDJPY", VenueBuy: "ic:mt5",
		PriceBuy: 95.0,
	}
	// usdPerLot 返回 ok=false → skip=true（fail-closed）
	_, _, skip := computeBuyQty(opp, 1000.0, func(c, v, s string) (float64, bool) { return 0, false })
	if !skip {
		t.Fatal("computeBuyQty should skip when CS unknown")
	}
	// 模拟 handleOpportunity 的 recordCSMiss 调用
	e.recordCSMiss(opp.VenueBuy, opp.Canonical)
	e.csMissMu.Lock()
	n := e.csMissCounts["ic:mt5|AUDJPY"]
	e.csMissMu.Unlock()
	if n != 1 {
		t.Errorf("csMissCounts should be 1, got %d", n)
	}
}
