// selfwatch_test.go —— D-033 pgstore 锚：cohort 口径、1h 去重、迁移幂等、Stage 闸读口。
package pgstore

import (
	"context"
	"testing"
	"time"

	"nox/migrations"
)

func TestD033_PaperWindowCohort(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE paper_spot_carry RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	since := time.Now().Add(-24 * time.Hour)
	mk := func(status, structure string, entry, exit time.Time) {
		_, err := s.Pool().Exec(ctx, `
			INSERT INTO paper_spot_carry (venue_a, venue_b, venue_type_a, venue_type_b, symbol, side, entry_funding_ann, qty_usd, last_funding_ann,
				entry_ts, status, structure, exit_ts, accrued_usd)
			VALUES ('3001157','okx','','perp',$1,'short_perp',10,1000,10,$2,$3,$4,$5,1.0)`,
			"SYM"+status+structure+entry.Format("15")+exit.Format("15"), entry, status, structure,
			nullIfZero(exit))
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	old := since.Add(-48 * time.Hour)
	mk("active", "long_cfd_short_perp", since.Add(time.Hour), time.Time{}) // 窗内新开
	mk("active", "long_cfd_short_perp", old, time.Time{})                  // 窗始 active
	mk("exited", "long_cfd_short_perp", old, since.Add(time.Hour))         // 窗内退出
	mk("exited", "long_cfd_short_perp", old.Add(-time.Hour), old)          // 窗前退出 → 不收
	mk("active", "spot_carry", since.Add(time.Hour), time.Time{})          // 他结构 → 不收

	rows, err := s.PaperWindowCohort(ctx, "long_cfd_short_perp", since)
	if err != nil {
		t.Fatal(err)
	}
	var act, ex int
	for _, r := range rows {
		switch r.Status {
		case "active":
			act++
		case "exited":
			ex++
		}
	}
	if act != 2 || ex != 1 || len(rows) != 3 {
		t.Fatalf("cohort: %d rows (act=%d ex=%d)", len(rows), act, ex)
	}
}

func TestD033_InsertAlertDedup1h(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE alerts RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	msg := "[自盯] paper accrue 停滞 3 行"
	for i := 0; i < 3; i++ {
		if err := s.InsertAlertDedup(ctx, "warn", msg, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM alerts WHERE level='warn'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("dedup: want 1 row, got %d", n)
	}
	// 窗内不同文案不落抑（不同 dedup 键）
	if err := s.InsertAlertDedup(ctx, "warn", "[自盯] trade_session facts 缺席", time.Hour); err != nil {
		t.Fatal(err)
	}
	// 陈旧同键（>1h）不再抑制
	if _, err := s.Pool().Exec(ctx, `UPDATE alerts SET ts = now() - interval '2 hours' WHERE level='warn'`); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertAlertDedup(ctx, "warn", msg, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM alerts WHERE level='warn'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("after window: want 3 rows, got %d", n)
	}
}

func TestD033_Migration173_Idempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// 先重放播种（同进程其他测试可能 TRUNCATE app_config——迁移幂等可重入）
	if _, err := s.Pool().Exec(ctx, migrations.PaperWindowStartSQL); err != nil {
		t.Fatalf("seed replay: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, migrations.EnvKeysNoxSQL); err != nil {
		t.Fatalf("1002 rename: %v", err)
	}
	v, ok, err := s.AppConfigString(ctx, "NOX_PAPER_WINDOW_START")
	if err != nil || !ok || v != "2026-10-03T01:59Z" {
		t.Fatalf("seed: %v %v %q", ok, err, v)
	}
	// 运维改值 → 重放迁移不覆盖
	if err := s.UpsertAppConfig(ctx, "NOX_PAPER_WINDOW_START", "2026-11-01T00:00Z", "运维改"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, migrations.PaperWindowStartSQL); err != nil {
		t.Fatalf("replay 0173: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, migrations.EnvKeysNoxSQL); err != nil {
		t.Fatalf("1002 rename: %v", err)
	}
	if v, _, _ := s.AppConfigString(ctx, "NOX_PAPER_WINDOW_START"); v != "2026-11-01T00:00Z" {
		t.Fatalf("override clobbered: %q", v)
	}
	// 新键空位播种语义：删掉行再重放 → 复活
	if _, err := s.Pool().Exec(ctx, `DELETE FROM app_config WHERE key='NOX_PAPER_WINDOW_START'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, migrations.PaperWindowStartSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, migrations.EnvKeysNoxSQL); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.AppConfigString(ctx, "NOX_PAPER_WINDOW_START"); v != "2026-10-03T01:59Z" {
		t.Fatalf("reseed: %q", v)
	}
}

func TestD033_StageReadPorts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE fin_venue_exposure_caps, fin_canonical_exposure_caps, xasset_allow_combos`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	vc, cc, err := s.ExposureCapCounts(ctx)
	if err != nil || vc != 0 || cc != 0 {
		t.Fatalf("caps empty: %d %d %v", vc, cc, err)
	}
	if n, err := s.XAssetAllowRealCount(ctx); err != nil || n != 0 {
		t.Fatalf("allow empty: %d %v", n, err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_venue_exposure_caps (venue_key, cap_usd, enabled) VALUES ('okx:perp', 840, true);
		INSERT INTO fin_canonical_exposure_caps (canonical, cap_usd, enabled) VALUES ('XAUUSD', 5000, true);
		INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_sell, max_long_qty, max_short_qty, max_gross_usd, enabled)
		VALUES ('XAUUSD','XAUUSDT','3001157','okx',1,1,6000,true)`); err != nil {
		t.Fatalf("seed caps: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_sell, max_long_qty, max_short_qty, max_gross_usd, enabled, simulated)
		VALUES ('XAGUSD','XAGUSDT','3001157','okx',1,1,6000,true,true)`); err != nil {
		t.Fatalf("seed sim combo: %v", err)
	}
	vc, cc, _ = s.ExposureCapCounts(ctx)
	ar, _ := s.XAssetAllowRealCount(ctx)
	if vc != 1 || cc != 1 || ar != 1 {
		t.Fatalf("counts: %d %d %d", vc, cc, ar)
	}
	// R1：实盘启用 venue 对（venue_key 生成列剥类型段 → 裸口径；simulated 行不计）
	pairs, perr := s.XAssetAllowRealPairs(ctx)
	if perr != nil || len(pairs) != 1 || pairs[0].VenueA != "3001157" || pairs[0].VenueB != "okx" {
		t.Fatalf("pairs: %+v %v", pairs, perr)
	}
	if mig, err := s.SchemaMigrationMax(ctx); err != nil || mig < 173 {
		t.Fatalf("migrations: %d %v", mig, err)
	}
}

func TestD033_MakerLegStats(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, position_settlements RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var pid int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO positions (opportunity_id, type, status, opened_at)
		VALUES ('D033:maker','XASSET_CARRY','OPEN', now()) RETURNING id`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO position_legs (position_id, leg_no, phase, attempt_no, venue, venue_type, symbol, side, qty, price_expected, status, fill_kind)
		VALUES ($1,0,'OPEN',1,'okx','perp','XAU-USDT-SWAP','SELL',1,2500,'FILLED','maker'),
		       ($1,1,'OPEN',1,'3001157','mt5','XAUUSDm','BUY',1,2500,'FILLED','taker')`, pid); err != nil {
		t.Fatal(err)
	}
	// 全腿零成交 FAILED + maker 腿（incidentGatePredicate：非事故）
	var fid int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO positions (opportunity_id, type, status, opened_at)
		VALUES ('D033:maker-fail','XASSET_CARRY','FAILED', now()) RETURNING id`).Scan(&fid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO position_legs (position_id, leg_no, phase, attempt_no, venue, venue_type, symbol, side, qty, price_expected, status, fill_kind)
		VALUES ($1,0,'OPEN',1,'okx','perp','XAU-USDT-SWAP','SELL',1,2500,'FAILED','maker')`, fid); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-time.Hour)
	opens, inc, err := s.MakerLegStatsSince(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	// fid 的 maker 腿 status=FAILED → opens 只计 FILLED maker 仓=1；零成交 FAILED 非事故 → inc=0
	if opens != 1 || inc != 0 {
		t.Fatalf("maker stats: opens=%d inc=%d", opens, inc)
	}
}

func nullIfZero(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t
}
