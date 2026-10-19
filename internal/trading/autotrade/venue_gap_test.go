package autotrade

import (
	"context"
	"testing"

	"nox/internal/platform/testutil/storetest"
)

// TestVenueGapSweep 新户入册缺口锚：
// ①在册 venue_key 缺放行+缺闸行 → condition_gap(venue_onboard,constraint) 落箱且幂等
// ②放行+闸行齐面 → 不落
func TestVenueGapSweep(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES ('gapnew','mt5','h',443,'x') ON CONFLICT (venue) DO NOTHING;
		 INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES ('gapfull','mt5','h',443,'x') ON CONFLICT (venue) DO NOTHING;
		 INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated, note) VALUES ('venue','gapfull:mt5',true,false,'t') ON CONFLICT DO NOTHING;
		 INSERT INTO fin_venue_exposure_caps (venue_key, cap_usd, enabled, note) VALUES ('gapfull:mt5',1000,true,'t') ON CONFLICT (venue_key) DO NOTHING;
		 DELETE FROM fin_condition_gap WHERE template_id='venue_onboard'`); err != nil {
		t.Fatal(err)
	}
	e := &Engine{store: st}
	allow := map[string]bool{"gapfull:mt5": true}
	e.venueGapSweep(ctx, allow)
	var n int
	var text string
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*), max(request_text) FROM fin_condition_gap WHERE template_id='venue_onboard' AND subject='gapnew:mt5'`).Scan(&n, &text); err != nil {
		t.Fatal(err)
	}
	if n != 1 || text == "" {
		t.Fatalf("缺放行+缺闸行应落 1 行 gap: n=%d text=%q", n, text)
	}
	var n2 int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_condition_gap WHERE template_id='venue_onboard' AND subject='gapfull:mt5'`).Scan(&n2); err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("齐面户不应落 gap: %d", n2)
	}
	// 幂等：重扫不重复建行
	e.venueGapSweep(ctx, allow)
	var n3 int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_condition_gap WHERE template_id='venue_onboard' AND subject='gapnew:mt5'`).Scan(&n3); err != nil {
		t.Fatal(err)
	}
	if n3 != 1 {
		t.Fatalf("幂等重扫应仍 1 行: %d", n3)
	}
}
