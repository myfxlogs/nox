package pgstore

import (
	"context"
	"testing"
	"time"
)

// d438Store 复用 testStore 并清空相关表。
func d438Store(t *testing.T) *Store {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE position_cashflows, income_events, positions, position_settlements, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

func d438InsertPos(t *testing.T, s *Store) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1, 'XASSET_CARRY', 'CLOSED', now()) RETURNING id`,
		"D438:XAU:vA:vB").Scan(&id); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	return id
}

func d438CF(t *testing.T, s *Store, posID int64, kind string, amt float64, quality string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_cashflows (position_id, kind, venue, amount_usd, ts, source, quality)
		 VALUES ($1,$2,'v', $3, now(), 'swapledger', $4)`,
		posID, kind, amt, quality); err != nil {
		t.Fatalf("insert cashflow %s: %v", kind, err)
	}
}

func d438Income(t *testing.T, s *Store, posID int64, amt float64) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO income_events (venue, venue_type, symbol, income_type, amount_usd, tran_id, ts, position_id)
		 VALUES ('okx','perp','XAU-USDT-SWAP','FUNDING_FEE', $1, $2, now(), $3)`,
		amt, time.Now().UnixNano(), posID); err != nil {
		t.Fatalf("insert income: %v", err)
	}
}

// T1：income 覆盖替换——funding 公式行 estimated + income 行 → income 替换，quality=actual。
func TestD438_T1_IncomeOverridesFormula(t *testing.T) {
	s := d438Store(t)
	ctx := context.Background()
	posID := d438InsertPos(t, s)
	d438CF(t, s, posID, "funding", 2.97, "estimated")
	d438Income(t, s, posID, 1.03)
	net, q, err := s.SumCashflowsNetForSettle(ctx, posID, true, true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !d193Approx(net, 1.03, 1e-9) {
		t.Fatalf("net=%v want 1.03（income 替换公式）", net)
	}
	if q != "actual" {
		t.Fatalf("quality=%s want actual", q)
	}
}

// T2：pos498 形态——funding 行已被升 actual + income 存在 → income 替换，公式行不进 net。
func TestD438_T2_Pos498Shape(t *testing.T) {
	s := d438Store(t)
	ctx := context.Background()
	posID := d438InsertPos(t, s)
	d438CF(t, s, posID, "funding", 5.3925, "actual") // resettle 错升后的形态
	d438CF(t, s, posID, "commission", -2.18, "actual")
	d438Income(t, s, posID, 2.7917)
	net, q, err := s.SumCashflowsNetForSettle(ctx, posID, true, true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := -2.18 + 2.7917 // 公式 $5.3925 不得进 net（无双计）
	if !d193Approx(net, want, 1e-9) {
		t.Fatalf("net=%v want %v", net, want)
	}
	if q != "actual" {
		t.Fatalf("quality=%s want actual", q)
	}
}

// T3：income=0 + funding 行存在 → 旧语义回落，公式额进 net，quality=estimated。
func TestD438_T3_FallbackFormula(t *testing.T) {
	s := d438Store(t)
	ctx := context.Background()
	posID := d438InsertPos(t, s)
	d438CF(t, s, posID, "funding", 2.97, "estimated")
	net, q, err := s.SumCashflowsNetForSettle(ctx, posID, true, true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !d193Approx(net, 2.97, 1e-9) {
		t.Fatalf("net=%v want 2.97", net)
	}
	if q != "estimated" {
		t.Fatalf("quality=%s want estimated", q)
	}
}

// T4：hasFundingLeg+fundingDue+零证据 → quality=unknown（D-331 不静默）。
func TestD438_T4_FundingDueUnknown(t *testing.T) {
	s := d438Store(t)
	ctx := context.Background()
	posID := d438InsertPos(t, s)
	_, q, err := s.SumCashflowsNetForSettle(ctx, posID, true, true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if q != "unknown" {
		t.Fatalf("quality=%s want unknown", q)
	}
}

// T5：hasFundingLeg+!fundingDue+零证据 → 不降 unknown；无任何行 → no_cashflows。
func TestD438_T5_ShortHoldNoCashflows(t *testing.T) {
	s := d438Store(t)
	ctx := context.Background()
	posID := d438InsertPos(t, s)
	net, q, err := s.SumCashflowsNetForSettle(ctx, posID, true, false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if net != 0 || q != "no_cashflows" {
		t.Fatalf("net=%v q=%s want 0/no_cashflows", net, q)
	}
}

// T6：!hasFundingLeg + income 行存在 → income 不计入（防御分支）。
func TestD438_T6_NoFundingLegIgnoresIncome(t *testing.T) {
	s := d438Store(t)
	ctx := context.Background()
	posID := d438InsertPos(t, s)
	d438Income(t, s, posID, 1.03)
	d438CF(t, s, posID, "commission", -0.5, "actual")
	net, q, err := s.SumCashflowsNetForSettle(ctx, posID, false, false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !d193Approx(net, -0.5, 1e-9) {
		t.Fatalf("net=%v want -0.5（income 不计入）", net)
	}
	if q != "actual" {
		t.Fatalf("quality=%s want actual", q)
	}
}
