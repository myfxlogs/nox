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

// TestVenueGapSweep_WaivedRevive D-632：删径级联把未决 gap 翻 waived（终态）后，
// 同键重入册（owner 重加户）撞 UNIQUE 静默吞复请——扫经 ReopenConditionGap 复活。
func TestVenueGapSweep_WaivedRevive(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES ('gapwaive','mt5','h',443,'x'),('gapownwaive','mt5','h',443,'x') ON CONFLICT (venue) DO NOTHING;
		 DELETE FROM fin_condition_gap WHERE template_id='venue_onboard' AND subject IN ('gapwaive:mt5','gapownwaive:mt5');
		 INSERT INTO fin_condition_gap (template_id, kind, subject, request_text, status, resolved_at, resolve_note) VALUES
		 ('venue_onboard','constraint','gapwaive:mt5','r','waived',now(),'删径级联自动 waive（venue_key 已删，禁硬删留痕）'),
		 ('venue_onboard','constraint','gapownwaive:mt5','r','waived',now(),'决策席裁：非 autotrade 执行目标池');`); err != nil {
		t.Fatal(err)
	}
	e := &Engine{store: st}
	e.venueGapSweep(ctx, map[string]bool{}) // gapwaive/gapownwaive:mt5 不在放行集→缺行缺口
	// 出处闸对抗锚：级联出处行复活、业主裁出处行粘住（无闸=业主 waive 被复活实证）。
	var stOwn string
	if err := st.Pool().QueryRow(ctx,
		`SELECT status FROM fin_condition_gap WHERE template_id='venue_onboard' AND subject='gapownwaive:mt5'`).Scan(&stOwn); err != nil {
		t.Fatal(err)
	}
	if stOwn != "waived" {
		t.Fatalf("业主裁 waived 行应粘住不复活: %s", stOwn)
	}
	var st1 string
	if err := st.Pool().QueryRow(ctx,
		`SELECT status FROM fin_condition_gap WHERE template_id='venue_onboard' AND subject='gapwaive:mt5'`).Scan(&st1); err != nil {
		t.Fatal(err)
	}
	if st1 != "requested" {
		t.Fatalf("waived 行重入册应复活回 requested: %s", st1)
	}
	// 已复流行重扫：requested 态两推闸皆 no-op，态稳（不弹 fulfilled/open 旁支）。
	e.venueGapSweep(ctx, map[string]bool{})
	if err := st.Pool().QueryRow(ctx,
		`SELECT status FROM fin_condition_gap WHERE template_id='venue_onboard' AND subject='gapwaive:mt5'`).Scan(&st1); err != nil {
		t.Fatal(err)
	}
	if st1 != "requested" {
		t.Fatalf("重扫态应稳在 requested: %s", st1)
	}
}
