package autotrade

import (
	"testing"
	"time"

	"arb/internal/combo"
	"arb/internal/store/pgstore"
)

// TestBuildCooldownSet_WithinCooldown 冷却内 → ComboKey 入集（跳过）。
func TestBuildCooldownSet_WithinCooldown(t *testing.T) {
	now := time.Now()
	ck := combo.Key("SWAPARB", "AUDJPY", "vA:mt5", "vB:mt5")
	closed := []pgstore.Position{
		{OpportunityID: ck + ":" + "1700000000000", ClosedAt: ptrTime(now.Add(-2 * 24 * time.Hour))}, // 2 天前 < 7 天冷却
	}
	set := buildCooldownSet(closed, 7, now)
	if _, ok := set[ck]; !ok {
		t.Errorf("ComboKey closed 2 days ago (within 7-day cooldown) should be in set")
	}
}

// TestBuildCooldownSet_Expired 冷却过期 → 不入集（放行）。
func TestBuildCooldownSet_Expired(t *testing.T) {
	now := time.Now()
	ck := combo.Key("SWAPARB", "AUDJPY", "vA:mt5", "vB:mt5")
	closed := []pgstore.Position{
		{OpportunityID: ck + ":" + "1700000000000", ClosedAt: ptrTime(now.Add(-10 * 24 * time.Hour))}, // 10 天前 > 7 天冷却
	}
	set := buildCooldownSet(closed, 7, now)
	if _, ok := set[ck]; ok {
		t.Errorf("ComboKey closed 10 days ago (expired 7-day cooldown) should NOT be in set")
	}
}

// TestBuildCooldownSet_NoClosedRecord 无 CLOSED 记录 → 空集（放行）。
func TestBuildCooldownSet_NoClosedRecord(t *testing.T) {
	set := buildCooldownSet(nil, 7, time.Now())
	if len(set) != 0 {
		t.Errorf("No closed records should yield empty set, got %d", len(set))
	}
}

// TestBuildCooldownSet_Disabled cooldownDays=0 → 关闭冷却（空集）。
func TestBuildCooldownSet_Disabled(t *testing.T) {
	now := time.Now()
	ck := combo.Key("SWAPARB", "AUDJPY", "vA:mt5", "vB:mt5")
	closed := []pgstore.Position{
		{OpportunityID: ck + ":" + "1700000000000", ClosedAt: ptrTime(now.Add(-1 * time.Hour))},
	}
	set := buildCooldownSet(closed, 0, now)
	if set != nil {
		t.Errorf("cooldownDays=0 should disable cooldown (nil set), got %v", set)
	}
}

// TestBuildCooldownSet_NilClosedAt 无 closed_at → 跳过（不入集）。
func TestBuildCooldownSet_NilClosedAt(t *testing.T) {
	now := time.Now()
	ck := combo.Key("SWAPARB", "AUDJPY", "vA:mt5", "vB:mt5")
	closed := []pgstore.Position{
		{OpportunityID: ck + ":" + "1700000000000", ClosedAt: nil}, // 无 closed_at
	}
	set := buildCooldownSet(closed, 7, now)
	if _, ok := set[ck]; ok {
		t.Errorf("Position without closed_at should be skipped (not in set)")
	}
}

// TestBuildCooldownSet_MultipleCloseTakeLatest 同 ComboKey 多次平仓取最近 closed_at。
func TestBuildCooldownSet_MultipleCloseTakeLatest(t *testing.T) {
	now := time.Now()
	ck := combo.Key("SWAPARB", "AUDJPY", "vA:mt5", "vB:mt5")
	closed := []pgstore.Position{
		{OpportunityID: ck + ":" + "1700000000000", ClosedAt: ptrTime(now.Add(-5 * 24 * time.Hour))}, // 5 天前
		{OpportunityID: ck + ":" + "1700000000001", ClosedAt: ptrTime(now.Add(-2 * 24 * time.Hour))}, // 2 天前（更近）
	}
	set := buildCooldownSet(closed, 7, now)
	got, ok := set[ck]
	if !ok {
		t.Fatal("ComboKey should be in set (within cooldown)")
	}
	want := now.Add(-2 * 24 * time.Hour)
	if got.Sub(want) > time.Second {
		t.Errorf("Multiple close should take latest closed_at; want ~%v, got %v", want, got)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
