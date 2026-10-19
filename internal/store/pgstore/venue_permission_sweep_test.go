package pgstore

// D-632 删对象径级联清扫锚：
// ①扫面四_face 逐面计数+非同键行/canonical 行/已收口 gap 零误伤
// ②历史台账零触（income_events 同键金丝雀行存活——D-631 ledger 层=append-only）
// ③exchange 级联：sim/real 双行删尽才扫面+幂等零扫
// ④清扫留痕告警落 alerts
import (
	"context"
	"testing"
)

func d632Cleanup(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM autotrade_allow_rules WHERE value LIKE 'd632%' OR value='D632SYM'`,
		`DELETE FROM xasset_allow_combos WHERE canonical_buy LIKE 'D632%'`,
		`DELETE FROM fin_venue_exposure_caps WHERE venue_key LIKE 'd632%'`,
		`DELETE FROM fin_condition_gap WHERE subject LIKE 'd632%'`,
		`DELETE FROM income_events WHERE symbol='D632CANARY'`,
		`DELETE FROM exchange_configs WHERE venue='d632ex'`,
		`DELETE FROM alerts WHERE message LIKE '%d632%'`,
	} {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
}

// TestD632_SweepVenuePermissionSurface 全 face 扫清+误伤面+历史层金丝雀。
func TestD632_SweepVenuePermissionSurface(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	d632Cleanup(t, s)
	defer d632Cleanup(t, s)
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated) VALUES
		('venue','d632acct:mt5',true,false),
		('venue','d632acct:mt5',true,true),
		('venue','d632other:mt5',true,false),
		('canonical','D632SYM',true,false);
		INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell,
			max_long_qty, max_short_qty, max_gross_usd, source) VALUES
		('D632A','D632B','d632acct','mt5','d632other','mt5',1,1,100,'manual_seed'),
		('D632C','D632D','d632other','mt5','d632acct','mt5',1,1,100,'manual_seed'),
		('D632E','D632F','d632other','mt5','d632other','mt5',1,1,100,'manual_seed');
		INSERT INTO fin_venue_exposure_caps (venue_key, cap_usd, enabled, note) VALUES
		('d632acct:mt5',100,true,'t'),('d632other:mt5',100,true,'t');
		INSERT INTO income_events (venue, venue_type, symbol, income_type, amount_usd, tran_id, ts) VALUES
		('d632acct','mt5','D632CANARY','test',1,920260001,now());
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// gap 四态：open 直插 / requested 经 Request / fulfilled 经 Resolve / 异键 open 对照。
	idOpen, err := s.OpenConditionGap(ctx, &ConditionGap{TemplateID: "t_d632_a", Kind: "permission",
		Subject: "d632acct:mt5", RequestText: "r"})
	if err != nil {
		t.Fatal(err)
	}
	idReq, err := s.OpenConditionGap(ctx, &ConditionGap{TemplateID: "t_d632_b", Kind: "constraint",
		Subject: "d632acct:mt5", RequestText: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequestConditionGap(ctx, idReq); err != nil {
		t.Fatal(err)
	}
	idFul, err := s.OpenConditionGap(ctx, &ConditionGap{TemplateID: "t_d632_c", Kind: "constraint",
		Subject: "d632acct:mt5", RequestText: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequestConditionGap(ctx, idFul); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveConditionGap(ctx, idFul, true, "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenConditionGap(ctx, &ConditionGap{TemplateID: "t_d632_d", Kind: "constraint",
		Subject: "d632other:mt5", RequestText: "r"}); err != nil {
		t.Fatal(err)
	}

	sw, err := s.SweepVenuePermissionSurface(ctx, "d632acct:mt5")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if sw.AllowRules != 2 || sw.Combos != 2 || sw.Caps != 1 || sw.GapsWaived != 2 {
		t.Fatalf("计数锚: %+v, want allow=2 combos=2 caps=1 gaps=2（venue+sim 双行/买卖双腿/闸行/open+requested）", sw)
	}
	// ①同键零孤儿+非同键零误伤
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM autotrade_allow_rules WHERE kind='venue' AND value='d632acct:mt5'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("同键放行行应零残: %d", n)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM autotrade_allow_rules WHERE value IN ('d632other:mt5','D632SYM')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("异键 venue 行+canonical 行应存活: %d", n)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM xasset_allow_combos WHERE venue_key_buy='d632acct:mt5' OR venue_key_sell='d632acct:mt5'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("同键 combo 应零残: %d", n)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM xasset_allow_combos WHERE canonical_buy='D632E'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("异键 combo 应存活: %d", n)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM fin_venue_exposure_caps WHERE venue_key='d632acct:mt5'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("同键闸行应零残: %d", n)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM fin_venue_exposure_caps WHERE venue_key='d632other:mt5'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("异键闸行应存活: %d", n)
	}
	// ②gap 态机：未决两行翻 waived+留痕 note；已收口/异键不动
	var st, note string
	if err := s.pool.QueryRow(ctx,
		`SELECT status, resolve_note FROM fin_condition_gap WHERE id=$1`, idOpen).Scan(&st, &note); err != nil {
		t.Fatal(err)
	}
	if st != "waived" || note == "" {
		t.Fatalf("open 行应翻 waived+留痕: %s/%q", st, note)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT status FROM fin_condition_gap WHERE id=$1`, idReq).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "waived" {
		t.Fatalf("requested 行应翻 waived: %s", st)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT status FROM fin_condition_gap WHERE id=$1`, idFul).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "fulfilled" {
		t.Fatalf("已 fulfilled 行不应动: %s", st)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT status FROM fin_condition_gap WHERE subject='d632other:mt5'`).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "open" {
		t.Fatalf("异键 gap 不应动: %s", st)
	}
	// ③历史台账零触（income 同键金丝雀行存活——D-631 ledger 层 append-only）
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM income_events WHERE symbol='D632CANARY'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("历史台账行零触被破: %d", n)
	}
	// ④清扫留痕告警
	var msg string
	if err := s.pool.QueryRow(ctx,
		`SELECT message FROM alerts WHERE message LIKE '%d632acct:mt5%' AND message LIKE '%删除级联%'
		 ORDER BY id DESC LIMIT 1`).Scan(&msg); err != nil {
		t.Fatalf("清扫留痕告警缺: %v", err)
	}
}

// TestD632_DeleteExchangeConfigCascade sim/real 双行删尽才扫面+幂等零扫。
func TestD632_DeleteExchangeConfigCascade(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	d632Cleanup(t, s)
	defer d632Cleanup(t, s)
	cfgSim := &ExchangeConfig{Venue: "d632ex", VenueType: "perp", APIKey: "k1", APISecret: "s1", Simulated: true}
	cfgReal := &ExchangeConfig{Venue: "d632ex", VenueType: "perp", APIKey: "k2", APISecret: "s2", Simulated: false}
	if err := s.InsertExchangeConfig(ctx, cfgSim); err != nil {
		t.Fatalf("insert sim: %v", err)
	}
	if err := s.InsertExchangeConfig(ctx, cfgReal); err != nil {
		t.Fatalf("insert real: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated) VALUES ('venue','d632ex:perp',true,false);
		INSERT INTO fin_venue_exposure_caps (venue_key, cap_usd, enabled, note) VALUES ('d632ex:perp',100,true,'t');
		INSERT INTO fin_condition_gap (template_id, kind, subject, request_text) VALUES
		('venue_onboard','constraint','d632ex:perp','r');
	`); err != nil {
		t.Fatalf("seed perm: %v", err)
	}
	// ①删 sim 行——real 孪生在册→只删行不扫面
	var n int
	sw, err := s.DeleteExchangeConfigCascade(ctx, cfgSim.ID)
	if err != nil {
		t.Fatalf("del sim: %v", err)
	}
	if sw.Any() {
		t.Fatalf("孪生在册不应扫面: %+v", sw)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM autotrade_allow_rules WHERE value='d632ex:perp'`).Scan(&n); n != 1 || err != nil {
		t.Fatalf("孪生在册放行行应存活: n=%d err=%v", n, err)
	}
	// ②删 real 行——键退役→四_face 扫清
	sw, err = s.DeleteExchangeConfigCascade(ctx, cfgReal.ID)
	if err != nil {
		t.Fatalf("del real: %v", err)
	}
	if sw.AllowRules != 1 || sw.Caps != 1 || sw.GapsWaived != 1 {
		t.Fatalf("键退役扫面计数: %+v, want rules=1 caps=1 gaps=1", sw)
	}
	var ncfg int
	if err := s.pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM autotrade_allow_rules WHERE value='d632ex:perp')
		      + (SELECT count(*) FROM fin_venue_exposure_caps WHERE venue_key='d632ex:perp')
		      + (SELECT count(*) FROM fin_condition_gap WHERE subject='d632ex:perp' AND status='open'),
		       (SELECT count(*) FROM exchange_configs WHERE venue='d632ex')`).Scan(&n, &ncfg); err != nil {
		t.Fatal(err)
	}
	if n != 0 || ncfg != 0 {
		t.Fatalf("键退役后权限面应零残+配置零行: n=%d cfg=%d", n, ncfg)
	}
	var st string
	if err := s.pool.QueryRow(ctx,
		`SELECT status FROM fin_condition_gap WHERE subject='d632ex:perp'`).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "waived" {
		t.Fatalf("未决 gap 应翻 waived 非硬删: %s", st)
	}
	// ③幂等：id 不存在=零扫零错（原删语义保持）
	sw, err = s.DeleteExchangeConfigCascade(ctx, cfgReal.ID)
	if err != nil || sw.Any() {
		t.Fatalf("幂等删应零扫零错: %+v %v", sw, err)
	}
}
