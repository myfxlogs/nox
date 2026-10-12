// live_gate_helpers_test.go —— setupLiveGateStore 同型复刻
// （原件随 gate_live_test 迁 internal/risk/gate，D-212 拆搬；gate 域 fixture）。
package autotrade

import (
	"context"
	"strings"
	"testing"

	"arb/internal/store/pgstore"
)

func setupLiveGateStore(t *testing.T, venueLong, venueShort string, longReal, shortReal bool) *pgstore.Store {
	t.Helper()
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_state, strategy_stats, autotrade_logs, positions, position_legs, position_cashflows, broker_accounts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("insert autotrade_state: %v", err)
	}
	insert := func(venue string, isReal bool) {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, is_real)
			 VALUES ($1, 'mt5', 'h', 443, 'p', 's', 'connected', $2) ON CONFLICT DO NOTHING`,
			venue, isReal); err != nil {
			t.Fatalf("insert broker_account %s: %v", venue, err)
		}
	}
	vL, _, _ := strings.Cut(venueLong, ":")
	vS, _, _ := strings.Cut(venueShort, ":")
	insert(vL, longReal)
	insert(vS, shortReal)
	return s
}
