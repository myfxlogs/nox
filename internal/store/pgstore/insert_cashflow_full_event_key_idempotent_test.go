package pgstore

import (
	"context"
	"math"
	"testing"
	"time"
)

// d193Approx 浮点近似比较（pgstore 测试本地辅助）。
func d193Approx(got, want, tol float64) bool {
	return math.Abs(got-want) <= tol
}

// d193CashflowTestStore 复用 testStore 并清空 cashflows/positions（FK 链）。
func d193CashflowTestStore(t *testing.T) *Store {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE position_cashflows, positions, position_settlements, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

// d193InsertPosition 建一条 CLOSED 持仓返回 id（cashflows FK 依赖）。
func d193InsertPosition(t *testing.T, s *Store) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1, 'SWAPARB', 'CLOSED', now()) RETURNING id`,
		"D193:SYM:vA:vB").Scan(&id); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	return id
}

// TestInsertCashflowFull_EventKey_Idempotent D-193-A S4：同 event_key 二次插入 won=false，不报错。
func TestInsertCashflowFull_EventKey_Idempotent(t *testing.T) {
	s := d193CashflowTestStore(t)
	ctx := context.Background()
	posID := d193InsertPosition(t, s)
	ek := "swap:529:0:20260902"
	legNo := int16(0)
	q := "actual"
	c := &Cashflow{
		PositionID: posID,
		LegNo:      &legNo,
		Kind:       "swap",
		Venue:      "529:mt5",
		AmountUSD:  3.5,
		Ts:         time.Now(),
		EventKey:   &ek,
		Quality:    &q,
	}
	won, err := s.InsertCashflowFull(ctx, c)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if !won {
		t.Fatal("first insert won=false, want true")
	}
	// 同 event_key 二次插入 → 幂等成功 won=false
	won2, err := s.InsertCashflowFull(ctx, c)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if won2 {
		t.Fatal("second insert won=true, want false (idempotent)")
	}
}

// TestSumCashflowsNetWithQuality_Actual D-193-A S4：全 actual → quality='actual'。
func TestSumCashflowsNetWithQuality_Actual(t *testing.T) {
	s := d193CashflowTestStore(t)
	ctx := context.Background()
	posID := d193InsertPosition(t, s)
	d193InsertCashflowRow(t, s, posID, "swap", 3.0, "actual")
	d193InsertCashflowRow(t, s, posID, "funding", 1.0, "actual")
	sum, q, err := s.SumCashflowsNetWithQuality(ctx, posID)
	if err != nil {
		t.Fatalf("SumCashflowsNetWithQuality: %v", err)
	}
	if q != "actual" {
		t.Errorf("quality=%q, want actual", q)
	}
	if !d193Approx(sum, 4.0, 0.001) {
		t.Errorf("sum=%.4f, want 4.0", sum)
	}
}

// TestSumCashflowsNetWithQuality_Unknown D-193-A S4：含 unknown → quality='unknown'。
func TestSumCashflowsNetWithQuality_Unknown(t *testing.T) {
	s := d193CashflowTestStore(t)
	ctx := context.Background()
	posID := d193InsertPosition(t, s)
	d193InsertCashflowRow(t, s, posID, "swap", 3.0, "actual")
	d193InsertCashflowRow(t, s, posID, "funding", 1.0, "unknown")
	sum, q, err := s.SumCashflowsNetWithQuality(ctx, posID)
	if err != nil {
		t.Fatalf("SumCashflowsNetWithQuality: %v", err)
	}
	if q != "unknown" {
		t.Errorf("quality=%q, want unknown", q)
	}
	// unknown 行不计入 sum（仅 actual+legacy 计入）
	if !d193Approx(sum, 3.0, 0.001) {
		t.Errorf("sum=%.4f, want 3.0 (unknown excluded)", sum)
	}
}

// TestSumCashflowsNet_QualityFilter D-193-A S4：estimated 行不被 SumCashflowsNet 统计。
func TestSumCashflowsNet_QualityFilter(t *testing.T) {
	s := d193CashflowTestStore(t)
	ctx := context.Background()
	posID := d193InsertPosition(t, s)
	d193InsertCashflowRow(t, s, posID, "swap", 3.0, "actual")
	d193InsertCashflowRow(t, s, posID, "funding", 5.0, "estimated")
	sum, err := s.SumCashflowsNet(ctx, posID)
	if err != nil {
		t.Fatalf("SumCashflowsNet: %v", err)
	}
	// estimated 行被过滤 → 只剩 actual 3.0
	if !d193Approx(sum, 3.0, 0.001) {
		t.Errorf("sum=%.4f, want 3.0 (estimated filtered)", sum)
	}
}

// TestListCashflows_LegacyNullTolerant D-193-A S4：legacy 行新字段 NULL 不 panic。
func TestListCashflows_LegacyNullTolerant(t *testing.T) {
	s := d193CashflowTestStore(t)
	ctx := context.Background()
	posID := d193InsertPosition(t, s)
	// 直接插 legacy 行（无新字段）
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_cashflows (position_id, kind, venue, venue_type, amount_usd, ts) VALUES ($1, 'swap', '529', 'mt5', 2.0, now())`,
		posID); err != nil {
		t.Fatalf("insert legacy cashflow: %v", err)
	}
	out, err := s.ListCashflows(ctx, posID)
	if err != nil {
		t.Fatalf("ListCashflows: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 cashflow, got %d", len(out))
	}
	c := out[0]
	if c.EventKey != nil {
		t.Errorf("legacy EventKey want nil, got %v", *c.EventKey)
	}
	if c.Quality != nil {
		t.Errorf("legacy Quality want nil, got %v", *c.Quality)
	}
	if !d193Approx(c.AmountUSD, 2.0, 0.001) {
		t.Errorf("amount=%.4f, want 2.0", c.AmountUSD)
	}
}

// d193InsertCashflowRow 直接插带 quality 的 cashflow 行（测试辅助）。
func d193InsertCashflowRow(t *testing.T, s *Store, posID int64, kind string, amount float64, quality string) {
	t.Helper()
	ctx := context.Background()
	ek := kind + ":" + string(rune('A'+int(posID%26))) + ":" + quality + ":" + time.Now().Format("150405.000000")
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_cashflows (position_id, kind, venue, venue_type, amount_usd, ts, quality, event_key)
		 VALUES ($1, $2, '529', 'mt5', $3, now(), $4, $5)`,
		posID, kind, amount, quality, ek); err != nil {
		t.Fatalf("insert cashflow row %s/%s: %v", kind, quality, err)
	}
}
