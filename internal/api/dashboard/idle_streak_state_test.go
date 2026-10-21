package dashboard

import (
	"errors"
	"testing"
	"time"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

// TestSummarizeIdleStreak 眠钱瓦片分态锚（10-21 修；同日判据修：Idle 收益域+
// 鲜度闸）：healthy_zero（鲜行全零眠）与 no_data（零行/读败/**全陈行**=断供）
// 显式分态——修前两态同显「—」不可判读；判据修前 BelowFloor 恒 false=永假健康零。
func TestSummarizeIdleStreak(t *testing.T) {
	now := time.Now()
	// 读败 → NO_DATA（断供不冒健康零）。
	st, d, p := summarizeIdleStreak(nil, errors.New("pg down"), now)
	if st != v1.IdleStreakState_IDLE_STREAK_STATE_NO_DATA || d != 0 || p != "" {
		t.Fatalf("读败应 NO_DATA: %v %d %q", st, d, p)
	}
	// 读模零行 → NO_DATA（D-612 首轮动窗前零行为预期=断供非健康）。
	st, _, _ = summarizeIdleStreak(map[string]pgstore.IdleStreak{}, nil, now)
	if st != v1.IdleStreakState_IDLE_STREAK_STATE_NO_DATA {
		t.Fatalf("零行应 NO_DATA: %v", st)
	}
	// 全陈行（sleepmon 停跑>48h 冻结旧行）→ NO_DATA——对抗锚：删鲜度闸此断言必红
	//（冻结 Days=0 行会冒 HEALTHY_ZERO=永假健康零第二形态）。
	st, _, _ = summarizeIdleStreak(map[string]pgstore.IdleStreak{
		"okx\x00unified": {Days: 0, CheckDay: now.Add(-96 * time.Hour)},
	}, nil, now)
	if st != v1.IdleStreakState_IDLE_STREAK_STATE_NO_DATA {
		t.Fatalf("全陈行应 NO_DATA（冻结行不冒健康零）: %v", st)
	}
	// 陈行+鲜行混存 → 只判鲜行（陈行 Days=9 不得劫持 STREAK）。
	st, d, _ = summarizeIdleStreak(map[string]pgstore.IdleStreak{
		"old\x00pool": {Days: 9, CheckDay: now.Add(-96 * time.Hour)},
		"okx\x00earn": {Days: 0, CheckDay: now},
	}, nil, now)
	if st != v1.IdleStreakState_IDLE_STREAK_STATE_HEALTHY_ZERO || d != 0 {
		t.Fatalf("混存应只判鲜行: %v %d", st, d)
	}
	// 在册零眠钱池 → HEALTHY_ZERO（真零≠断供）。
	st, d, _ = summarizeIdleStreak(map[string]pgstore.IdleStreak{
		"okx\x00unified": {Venue: "okx", VenueType: "unified", Days: 0, CheckDay: now},
	}, nil, now)
	if st != v1.IdleStreakState_IDLE_STREAK_STATE_HEALTHY_ZERO || d != 0 {
		t.Fatalf("零眠钱应 HEALTHY_ZERO: %v %d", st, d)
	}
	// 有眠钱池 → STREAK 携最长天数+池键。
	st, d, p = summarizeIdleStreak(map[string]pgstore.IdleStreak{
		"okx\x00unified": {Days: 2, CheckDay: now},
		"mt5\x00xm":      {Days: 7, CheckDay: now},
	}, nil, now)
	if st != v1.IdleStreakState_IDLE_STREAK_STATE_STREAK || d != 7 || p != "mt5\x00xm" {
		t.Fatalf("眠钱应 STREAK 携最长池: %v %d %q", st, d, p)
	}
}
