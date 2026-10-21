package dashboard

import (
	"errors"
	"testing"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

// TestSummarizeIdleStreak 眠钱瓦片分态锚（10-21 修）：
// healthy_zero（读模有行零破地板）与 no_data（零行/读败=断供）显式分态——
// 修前两态同显「—」不可判读。
func TestSummarizeIdleStreak(t *testing.T) {
	// 读败 → NO_DATA（断供不冒健康零）。
	st, d, p := summarizeIdleStreak(nil, errors.New("pg down"))
	if st != v1.IdleStreakState_IDLE_STREAK_STATE_NO_DATA || d != 0 || p != "" {
		t.Fatalf("读败应 NO_DATA: %v %d %q", st, d, p)
	}
	// 读模零行 → NO_DATA（D-612 首轮动窗前零行为预期=断供非健康）。
	st, _, _ = summarizeIdleStreak(map[string]pgstore.IdleStreak{}, nil)
	if st != v1.IdleStreakState_IDLE_STREAK_STATE_NO_DATA {
		t.Fatalf("零行应 NO_DATA: %v", st)
	}
	// 在册零破地板池 → HEALTHY_ZERO（真零≠断供）。
	st, d, _ = summarizeIdleStreak(map[string]pgstore.IdleStreak{
		"okx\x00unified": {Venue: "okx", VenueType: "unified", Days: 0},
	}, nil)
	if st != v1.IdleStreakState_IDLE_STREAK_STATE_HEALTHY_ZERO || d != 0 {
		t.Fatalf("零破地板应 HEALTHY_ZERO: %v %d", st, d)
	}
	// 有破地板池 → STREAK 携最长天数+池键。
	st, d, p = summarizeIdleStreak(map[string]pgstore.IdleStreak{
		"okx\x00unified": {Days: 2},
		"mt5\x00xm":      {Days: 7},
	}, nil)
	if st != v1.IdleStreakState_IDLE_STREAK_STATE_STREAK || d != 7 || p != "mt5\x00xm" {
		t.Fatalf("破地板应 STREAK 携最长池: %v %d %q", st, d, p)
	}
}
