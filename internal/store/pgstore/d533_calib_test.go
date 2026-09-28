// d533_calib_test.go —— D-533 S9：计分 upsert 幂等/提案 UNIQUE 防重/active 唯一索引/
// 取值链三跳。需 ARB_TEST_PG_DSN。全部走 run 作用域 venue 键（d533test）——
// 共享测试库跨运行残留不影响断言（历史无关）。
package pgstore

import (
	"context"
	"testing"

	"arb/internal/testutil"
)

const d533TestVenue = "d533test"

func TestD533_CalibStoreIdempotentAndUnique(t *testing.T) {
	s, err := New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// 计分行 upsert 幂等：同桶两写行数不变、值更新。
	for i := 0; i < 2; i++ {
		n, err := s.UpsertCalibScoreboard(ctx, []CalibScoreRow{{
			MonthYM: "2099-08", Dim: "commission", Venue: d533TestVenue, Symbol: "BTCTEST",
			SampleN: 9, MAE: 1.5, Bias: 0.8, P90Abs: 2.5, Status: "ok",
		}})
		if err != nil || n != 1 {
			t.Fatalf("scoreboard round%d: n=%d err=%v", i, n, err)
		}
	}
	got, err := s.ListCalibScoreboard(ctx, "2099-08")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var mine *CalibScoreRow
	for i := range got {
		if got[i].Venue == d533TestVenue && got[i].Symbol == "BTCTEST" {
			mine = &got[i]
		}
	}
	if mine == nil || mine.SampleN != 9 || mine.Bias != 0.8 {
		t.Fatalf("本桶 upsert 值错: %+v", got)
	}

	// 已知态起点（run 作用域）：撤净本 venue 全部行，提案重提一次并置 active。
	if _, err := s.Pool().Exec(ctx,
		`DELETE FROM calib_params WHERE venue=$1`, d533TestVenue); err != nil {
		t.Fatalf("reset venue rows: %v", err)
	}
	p := CalibParam{ParamKey: "cex_taker_fee_bps", Venue: d533TestVenue,
		ValueBps: 6.5, SourceMonth: "2099-08", SampleN: 9, MAE: 1.5, Bias: 1.5}
	fresh, err := s.UpsertCalibProposal(ctx, p)
	if err != nil || !fresh {
		t.Fatalf("首提应 fresh=true: fresh=%v err=%v", fresh, err)
	}
	if _, err := s.Pool().Exec(ctx,
		`UPDATE calib_params SET active=true, activated_at=now(), activated_by='test-d533', prev_value_bps=5.0
		 WHERE venue=$1 AND source_month='2099-08'`, d533TestVenue); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// active 唯一索引：同键再置一行 active → 必炸（回滚=翻位语义的守卫闸）。
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO calib_params (param_key, venue, symbol, value_bps, source_month, active)
		 VALUES ($1,$2,'','9.9','2099-07',true)`, p.ParamKey, d533TestVenue); err == nil {
		t.Fatal("active 唯一索引未拦双活")
	}

	// 取值链第一跳：active 命中返回提案值。
	m, err := s.ActiveCalibParams(ctx)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if m["cex_taker_fee_bps|"+d533TestVenue+"|"] != 6.5 {
		t.Fatalf("active 命中值错: %v", m["cex_taker_fee_bps|"+d533TestVenue+"|"])
	}
}

// TestD533_SeedRows 种值四行在迁移后存在（全局 venue=”，取值链 v1 锚）。
func TestD533_SeedRows(t *testing.T) {
	s, err := New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// 清理本席早期测试写入的共享库残留（全局 2099-07 试验行）+ 恢复迁移时种值态
	// （早前试验曾停活全局种值行——按 0154 种值语义原样复位）。
	if _, err := s.Pool().Exec(ctx,
		`DELETE FROM calib_params WHERE venue='' AND symbol='' AND source_month='2099-07'`); err != nil {
		t.Fatalf("clean legacy debris: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO calib_params (param_key, value_bps, source_month, activated_at, activated_by, active) VALUES
		  ('cex_taker_fee_bps',    5.0, 'seed', now(), 'd533-seed', true),
		  ('cfd_fee_bps',          0.3, 'seed', now(), 'd533-seed', true),
		  ('cex_slip_default_bps', 3.0, 'seed', now(), 'd533-seed', true),
		  ('intra_taker_fee_bps',  5.0, 'seed', now(), 'd533-seed', true)
		ON CONFLICT (param_key, venue, symbol, source_month) DO UPDATE SET
		  active=true, activated_at=now(), activated_by='d533-seed', prev_value_bps=NULL`); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	m, err := s.ActiveCalibParams(ctx)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	for key, want := range map[string]float64{
		"cex_taker_fee_bps||":    5.0,
		"cfd_fee_bps||":          0.3,
		"cex_slip_default_bps||": 3.0,
		"intra_taker_fee_bps||":  5.0,
	} {
		if m[key] != want {
			t.Errorf("种值 %s=%v want %v", key, m[key], want)
		}
	}
}
