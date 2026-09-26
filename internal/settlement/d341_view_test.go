package settlement

// D-341 T6：v_cycle_realrate 视图三列锚（488/489 历史仓回放形态：estimated=real facts 账/
// simulated=income_events/convergence=realized−simulated）。
import (
	"context"
	"math"
	"testing"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

func TestD341_CycleRealrateView(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	s, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE positions, position_legs, position_cashflows, income_events CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	pos := pgstore.Position{OpportunityID: "T6:XAUUSD:a:b", Type: "XASSET_CARRY", Status: "CLOSED", RealizedPnl: &[]float64{-2.46}[0]}
	posID, err := s.CreatePosition(ctx, &pos)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// estimated（real facts 账）：swapledger 两行 swap/funding；simulated：income_events 一行
	for _, cf := range []struct {
		kind string
		amt  float64
	}{{"swap", 1.53}, {"funding", 2.0}} {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO position_cashflows (position_id, leg_no, kind, venue, venue_type, amount_usd, ts)
			 VALUES ($1, 1, $2, 'binance','perp', $3, now())`, posID, cf.kind, cf.amt); err != nil {
			t.Fatalf("seed cf: %v", err)
		}
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO income_events (venue, venue_type, symbol, income_type, amount_usd, tran_id, ts, position_id)
		 VALUES ('binance', 'perp', 'XAUUSDT', 'FUNDING_FEE', 4.0, 777001, now(), $1)`, posID); err != nil {
		t.Fatalf("seed income: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`UPDATE positions SET exit_kind='stop', realized_pnl=-2.46 WHERE id=$1`, posID); err != nil {
		t.Fatal(err) // CreatePosition 不落 realized（列外）→ UPDATE 补
	}
	var est, sim, conv float64
	var exitKind *string
	if err := s.Pool().QueryRow(ctx,
		`SELECT estimated_carry_usd, simulated_carry_usd, cycle_convergence_usd, exit_kind
		 FROM v_cycle_realrate WHERE position_id=$1`, posID).Scan(&est, &sim, &conv, &exitKind); err != nil {
		t.Fatalf("view query: %v（红=migration 0106 视图未落）", err)
	}
	if math.Abs(est-3.53) > 1e-9 || math.Abs(sim-4.0) > 1e-9 || math.Abs(conv-(-6.46)) > 0.01 ||
		exitKind == nil || *exitKind != "stop" {
		t.Errorf("T6 锚: est=%v sim=%v conv=%v exit=%v, want 3.53/4.0/−6.46/stop", est, sim, conv, exitKind)
	}
}
