// d061_stage1_test.go —— D-061 ⑤验收锚：Stage 1 条件制判据——
// 行存活 47h 不 ✅ / 48h ✅（accrued>0 为伴生条件）；周开盘后 1h 不 ✅（warmup 2h）；
// 财历禁区内不 ✅。判据行经 BuildDaily 全链（fake store 注入）。
package selfwatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// d061AliveRow 白名单组合（stage1Pair 四元组）的 active 纸面行。
func d061AliveRow(entryAgo time.Duration, accrued float64) []pgstore.PaperSpotCarry {
	return []pgstore.PaperSpotCarry{{
		VenueA: "3001157", VenueB: "okx", Symbol: "XAU", Structure: "long_cfd_short_perp",
		Status: "active", EntryTs: tNow.Add(-entryAgo), AccruedUSD: accrued,
	}}
}

func d061ReadyExceptRow() Deps {
	st := greenStore()
	st.activeRows = nil
	return testDeps(st, goodEval(), tNow)
}

// TestD061_RowAlive47hNotReady 存活 47h（<48h）→ 存活行 ❌ → Stage 1 不就绪；
// accrued=0 同样 ❌（伴生条件）。
func TestD061_RowAlive47hNotReady(t *testing.T) {
	d := d061ReadyExceptRow()
	st := d.Store.(*fakeStore)
	st.activeRows = d061AliveRow(47*time.Hour, 3.2)
	rep, err := BuildDaily(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Body, "❌ 目标组合纸面行存活") {
		t.Fatalf("47h 应 ❌:\n%s", rep.Body)
	}
	if rep.Stage1OK {
		t.Fatal("47h 不得判就绪")
	}
	// accrued=0：age 达标仍 ❌。
	st.activeRows = d061AliveRow(50*time.Hour, 0)
	rep, err = BuildDaily(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Body, "❌ 目标组合纸面行存活") {
		t.Fatalf("accrued=0 应 ❌:\n%s", rep.Body)
	}
}

// TestD061_RowAlive48hReady 存活 48h（≥ 下限）且 accrued>0 → 存活行 ✅、
// 全表其余条件全绿 → Stage 1 就绪。
func TestD061_RowAlive48hReady(t *testing.T) {
	d := d061ReadyExceptRow()
	d.Store.(*fakeStore).activeRows = d061AliveRow(48*time.Hour, 3.2)
	rep, err := BuildDaily(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Body, "✅ 目标组合纸面行存活") {
		t.Fatalf("48h 应 ✅:\n%s", rep.Body)
	}
	if !rep.Stage1OK {
		t.Fatalf("条件全达标应就绪:\n%s", rep.Body)
	}
	if !strings.Contains(rep.Body, "XAU 3001157×okx：48h / $3.20") {
		t.Fatalf("存活数据行应书四元组+时长+计息:\n%s", rep.Body)
	}
}

// TestD061_OpenTimingWarmupNotReady 周开盘后 1h（< warmup 2h）→ 开仓时点 ❌。
// 直测判据函数：startMin=0（周一 00:00 UTC 开盘），now=周一 01:00 → elapsed=1h。
func TestD061_OpenTimingWarmupNotReady(t *testing.T) {
	st := greenStore()
	d := testDeps(st, goodEval(), tNow)
	mon0100 := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC) // 周一 01:00（开盘后 1h）
	row := stage1OpenTimingCheck(context.Background(), d, []pgstore.XAssetRealCombo{stage1Pair}, mon0100)
	if row.OK || !strings.Contains(row.Data, "距周开盘 1.0h") {
		t.Fatalf("暖场 1h 应 ❌: %+v %s", row.OK, row.Data)
	}
	// 对照：开盘后 3h（≥2h）→ ✅。
	mon0300 := time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)
	row = stage1OpenTimingCheck(context.Background(), d, []pgstore.XAssetRealCombo{stage1Pair}, mon0300)
	if !row.OK {
		t.Fatalf("开盘后 3h 应 ✅: %s", row.Data)
	}
}

// TestD061_BlackoutNotReady 财历禁区内（±15min 窗内有 high 事件）→ 开仓时点 ❌。
func TestD061_BlackoutNotReady(t *testing.T) {
	d := d061ReadyExceptRow()
	st := d.Store.(*fakeStore)
	st.activeRows = d061AliveRow(49*time.Hour, 3.2)
	name := "非农就业"
	st.macroEvents = []pgstore.MacroEvent{{
		Importance: "high", Currency: "USD", Name: name,
		EventTime: tNow.Add(-3 * time.Minute),
	}}
	rep, err := BuildDaily(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Body, "❌ 开仓时点") || !strings.Contains(rep.Body, "财历禁区=有") {
		t.Fatalf("禁区内应 ❌:\n%s", rep.Body)
	}
	if rep.Stage1OK {
		t.Fatal("禁区内不得判就绪")
	}
}
