// Cooler 单测（D-301c S3，C1-C4）：纯单测无 DB 依赖；时钟注入驱动窗口语义。
package alertcool

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// newTestCooler 固定起点时钟（测试内手动推进 base）。
func newTestCooler(window time.Duration, base *time.Time) *Cooler {
	c := New(window)
	c.now = func() time.Time { return *base }
	return c
}

// TestNormalize_CountsCollapseSameFingerprint C1：计数递增同指纹——连败告警的递增计数
// 归一后不再产生新指纹（72 条/时噪音根因）。
func TestNormalize_CountsCollapseSameFingerprint(t *testing.T) {
	a := Normalize("deribit collect failed 3 consecutive: dial tcp 192.229.85.35:8443 i/o timeout 5000ms")
	b := Normalize("deribit collect failed 72 consecutive: dial tcp 192.229.85.35:8443 i/o timeout 5000ms")
	if a != b {
		t.Fatalf("C1: count increment want same fingerprint, got\n a=%q\n b=%q", a, b)
	}
	if !containsAll(a, "#n", "#ip", "#dur") {
		t.Errorf("C1: want placeholders #n/#ip/#dur all applied, got %q", a)
	}
}

// TestAllow_WindowSemantics C2：首条放行→窗口内同指纹拒绝→越窗再放行（时钟推进跨 30min）。
func TestAllow_WindowSemantics(t *testing.T) {
	base := time.Unix(1700000000, 0)
	c := newTestCooler(30*time.Minute, &base)
	const lvl, msg = "warn", "deribit collect failed 3 consecutive: x"

	if !c.Allow(lvl, msg) {
		t.Fatal("C2: first allow want true")
	}
	base = base.Add(10 * time.Minute)
	if c.Allow(lvl, msg) {
		t.Fatal("C2: inside window same fingerprint want false")
	}
	base = base.Add(21 * time.Minute) // 累计 31m > 30m 窗
	if !c.Allow(lvl, msg) {
		t.Fatal("C2: past window want true again")
	}
}

// TestAllow_DistinctFingerprintsUnaffected C3：不同 level/不同 venue 字面 → 异指纹互不影响。
func TestAllow_DistinctFingerprintsUnaffected(t *testing.T) {
	base := time.Unix(1700000000, 0)
	c := newTestCooler(30*time.Minute, &base)
	if !c.Allow("warn", "venueA collect failed 3 consecutive") {
		t.Fatal("C3: first allow want true")
	}
	if !c.Allow("critical", "venueA collect failed 3 consecutive") {
		t.Fatal("C3: different level want independent (state transitions unaffected)")
	}
	if !c.Allow("warn", "venueB collect failed 3 consecutive") {
		t.Fatal("C3: different venue literal want independent")
	}
	// 原指纹此刻仍应被抑制
	if c.Allow("warn", "venueA collect failed 4 consecutive") {
		t.Fatal("C3: same normalized fingerprint want suppressed")
	}
}

// TestWrappers_SuppressedNotReachBottom C4：wrap 后被抑制的调用不落底层（计数断言）；
// Wrap2 抑制返回 nil、放行透传原 error；Wrap3 posID 不入指纹。
func TestWrappers_SuppressedNotReachBottom(t *testing.T) {
	base := time.Unix(1700000000, 0)
	c := newTestCooler(30*time.Minute, &base)

	// Wrap1：5 次同指纹只落底 1 次
	calls := 0
	w1 := c.Wrap1(func(level, msg string) { calls++ })
	for i := 0; i < 5; i++ {
		w1("warn", "collect failed 3 consecutive")
	}
	if calls != 1 {
		t.Fatalf("C4 Wrap1: want 1 bottom call out of 5, got %d", calls)
	}

	// Wrap2：抑制返回 nil 不落底；放行透传原 error
	sentinel := errors.New("insert failed")
	var calls2 int
	w2 := c.Wrap2(func(level, msg string) error { calls2++; return sentinel })
	if err := w2("warn", "repo spike 3 pct"); err != sentinel {
		t.Fatalf("C4 Wrap2: passthrough want sentinel, got %v", err)
	}
	if err := w2("warn", "repo spike 4 pct"); err != nil {
		t.Fatalf("C4 Wrap2: suppressed want nil, got %v", err)
	}
	if calls2 != 1 {
		t.Fatalf("C4 Wrap2: want 1 bottom call, got %d", calls2)
	}

	// Wrap3：posID 不同、消息同指纹 → 抑制（posID 不入指纹）
	idA, idB := int64(7), int64(9)
	calls3 := 0
	w3 := c.Wrap3(func(posID *int64, level, msg string) { calls3++ })
	w3(&idA, "critical", "close rejected insufficient margin")
	w3(&idB, "critical", "close rejected insufficient margin")
	if calls3 != 1 {
		t.Fatalf("C4 Wrap3: posID not in fingerprint want 1 bottom call, got %d", calls3)
	}
}

// containsAll 断言 s 含全部子串。
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
