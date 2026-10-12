package pgstore

import (
	"context"
	"testing"
	"time"
)

// D-111: ListComboDaily 排序修复（DESC）集成测试（DSN-gated）。

func TestListComboDaily_DescOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE combo_daily_rollup"); err != nil {
		t.Fatalf("TRUNCATE combo_daily_rollup: %v", err)
	}

	// 写入 3 条不同 day 的行（8-25, 8-26, 8-27）。
	comboKey := "D111TEST:ETHUSDT:venueA:venueB"
	days := []time.Time{
		time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC),
	}
	rows := make([]ComboDailyRow, len(days))
	for i, d := range days {
		netAvg := float64(40 + i) // 40, 41, 42
		rows[i] = ComboDailyRow{
			ComboKey: comboKey,
			Day:      d,
			Type:     "CROSS_VENUE",
			Samples:  1,
			NetAvg:   netAvg,
			NetMin:   netAvg,
			NetMax:   netAvg,
			FirstAt:  d,
			LastAt:   d,
		}
	}
	if err := s.UpsertComboDaily(ctx, rows); err != nil {
		t.Fatalf("UpsertComboDaily: %v", err)
	}

	// limit=1 → 返回最新一天（8-27, net_avg=42）
	got, err := s.ListComboDaily(ctx, comboKey, 1)
	if err != nil {
		t.Fatalf("ListComboDaily: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	if !got[0].Day.Equal(days[2]) {
		t.Fatalf("want day=8-27 (latest), got %v", got[0].Day)
	}
	if got[0].NetAvg != 42 {
		t.Fatalf("want net_avg=42, got %.4f", got[0].NetAvg)
	}

	// limit=0(=365) → 全量倒序（8-27, 8-26, 8-25）
	all, err := s.ListComboDaily(ctx, comboKey, 0)
	if err != nil {
		t.Fatalf("ListComboDaily all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 rows, got %d", len(all))
	}
	if !all[0].Day.Equal(days[2]) || !all[1].Day.Equal(days[1]) || !all[2].Day.Equal(days[0]) {
		t.Fatalf("want DESC order [8-27, 8-26, 8-25], got [%v, %v, %v]", all[0].Day, all[1].Day, all[2].Day)
	}
}
