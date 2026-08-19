package pgstore

import (
	"context"
	"testing"
	"time"
)

// TestAddDailyIncome_Upsert：合法写入返回 id + 同 day 重复 upsert 覆盖（值更新、不新增行、id 不变）。
func TestAddDailyIncome_Upsert(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	day := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)

	price := 0.33
	pnl := 31.0
	id1, err := s.AddDailyIncome(ctx, day, 5, 1, 0, &price, &pnl, "first")
	if err != nil {
		t.Fatalf("AddDailyIncome first: %v", err)
	}
	if id1 <= 0 {
		t.Errorf("first id want > 0, got %d", id1)
	}

	// 同 day 重复 → 覆盖：值更新、price/pnl 可清为 NULL、不新增行、id 不变。
	id2, err := s.AddDailyIncome(ctx, day, 6, 2, 2.0, nil, nil, "updated")
	if err != nil {
		t.Fatalf("AddDailyIncome second: %v", err)
	}
	if id2 != id1 {
		t.Errorf("upsert want same row id %d, got %d", id1, id2)
	}

	rows, err := s.ListDailyIncome(ctx, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("ListDailyIncome: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("upsert want 1 row, got %d", len(rows))
	}
	d := rows[0]
	if d.Day.Format("2006-01-02") != "2026-08-17" {
		t.Errorf("day mismatch: %v", d.Day)
	}
	if d.EnergyTrx != 6 || d.VotingTrx != 2 || d.InterestUSD != 2.0 || d.Note != "updated" {
		t.Errorf("upsert values not updated: %+v", d)
	}
	if d.Price != nil || d.PnlUSD != nil {
		t.Errorf("upsert price/pnl want NULL, got %v/%v", d.Price, d.PnlUSD)
	}
	if d.CreatedAt.IsZero() {
		t.Errorf("created_at want non-zero")
	}
}

// TestListDailyIncome_OrderRangeLimit：day 倒序 + from/to 区间 + limit/offset + limit≤0 默认 200。
func TestListDailyIncome_OrderRangeLimit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	days := []time.Time{
		time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC),
	}
	for i, day := range days {
		if _, err := s.AddDailyIncome(ctx, day, float64(i+1), 0, 0, nil, nil, ""); err != nil {
			t.Fatalf("seed day %d: %v", i, err)
		}
	}

	// 全量 → day 倒序
	all, err := s.ListDailyIncome(ctx, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 rows, got %d", len(all))
	}
	wantOrder := []string{"2026-08-17", "2026-08-16", "2026-08-15"}
	for i, w := range wantOrder {
		if got := all[i].Day.Format("2006-01-02"); got != w {
			t.Errorf("order[%d] want %s, got %s", i, w, got)
		}
	}

	// from/to 区间：[08-16, 08-17] → 2 行
	from := days[1]
	to := days[2]
	rng, err := s.ListDailyIncome(ctx, &from, &to, 0, 0)
	if err != nil {
		t.Fatalf("List range: %v", err)
	}
	if len(rng) != 2 {
		t.Fatalf("range want 2 rows, got %d", len(rng))
	}

	// limit 1 → 最新 1 行（08-17）
	lim, err := s.ListDailyIncome(ctx, nil, nil, 1, 0)
	if err != nil {
		t.Fatalf("List limit: %v", err)
	}
	if len(lim) != 1 || lim[0].Day.Format("2006-01-02") != "2026-08-17" {
		t.Errorf("limit 1 want latest day, got %d rows [%+v]", len(lim), lim)
	}

	// offset 1 limit 1 → 跳过最新，取 08-16
	pg, err := s.ListDailyIncome(ctx, nil, nil, 1, 1)
	if err != nil {
		t.Fatalf("List offset: %v", err)
	}
	if len(pg) != 1 || pg[0].Day.Format("2006-01-02") != "2026-08-16" {
		t.Errorf("offset 1 limit 1 want 08-16, got %+v", pg)
	}
}

// TestSumDailyIncomeInterest：全量 SUM(interest_usd)；空表 → 0。
func TestSumDailyIncomeInterest(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	z, err := s.SumDailyIncomeInterest(ctx)
	if err != nil {
		t.Fatalf("Sum empty: %v", err)
	}
	if z != 0 {
		t.Errorf("empty sum want 0, got %v", z)
	}

	days := []time.Time{
		time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC),
	}
	for i, day := range days {
		if _, err := s.AddDailyIncome(ctx, day, 0, 0, float64(i+1), nil, nil, ""); err != nil {
			t.Fatalf("seed day %d: %v", i, err)
		}
	}
	sum, err := s.SumDailyIncomeInterest(ctx)
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if sum != 6.0 {
		t.Errorf("sum want 6.0, got %v", sum)
	}
}
