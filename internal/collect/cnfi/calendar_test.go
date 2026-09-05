package cnfi

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// 2026 日历种子测试：官方通知（国办发明电〔2025〕7号）逐类锚点 + 总数硬断言。
func TestSeedCalendar2026_OfficialAnchors(t *testing.T) {
	st, err := pgstore.New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore: %v", err)
	}
	t.Cleanup(st.Close)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_calendar WHERE cal_version='guobanfa-2025-7'`)
	})
	business, total, err := SeedCalendar2026(ctx, st)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if total != 365 {
		t.Fatalf("total = %d, want 365", total)
	}
	// 2026 年营业日硬数：365 − 104(周末) + 6(调休上班) − 19(落在周中的假日) = 248
	if business != 248 {
		t.Fatalf("business = %d, want 248", business)
	}
	isBiz := func(day string) bool {
		var b bool
		if err := st.Pool().QueryRow(ctx,
			`SELECT is_business_day FROM cnfi_calendar WHERE cal_date=$1`, day).Scan(&b); err != nil {
			t.Fatalf("query %s: %v", day, err)
		}
		return b
	}
	cases := map[string]bool{
		"2026-01-01": false, // 元旦
		"2026-01-04": true,  // 元旦调休上班（周日）
		"2026-02-17": false, // 春节中段
		"2026-02-14": true,  // 春节调休上班（周六）
		"2026-02-28": true,  // 春节调休上班（周六）
		"2026-04-06": false, // 清明（周一）
		"2026-05-09": true,  // 劳动节调休上班（周六）
		"2026-06-19": false, // 端午（周五）
		"2026-09-20": true,  // 国庆调休上班（周日）
		"2026-10-01": false, // 国庆
		"2026-10-10": true,  // 国庆调休上班（周六）
		"2026-06-06": false, // 普通周六
		"2026-06-08": true,  // 普通周一
		"2026-12-31": true,  // 年末（周四）
	}
	for day, want := range cases {
		if got := isBiz(day); got != want {
			t.Fatalf("%s is_business_day = %v, want %v", day, got, want)
		}
	}
}

func TestSeedLayerTargets(t *testing.T) {
	st, err := pgstore.New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore: %v", err)
	}
	t.Cleanup(st.Close)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_signal_config WHERE key='layer_targets'`)
	})
	if err := SeedLayerTargets(ctx, st); err != nil {
		t.Fatalf("seed: %v", err)
	}
	targets, err := st.CNFILayerTargets(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if targets["liquidity"] != 20 || targets["coupon"] != 50 || targets["locked"] != 15 || targets["opportunity"] != 15 {
		t.Fatalf("targets = %v", targets)
	}
	var ref string
	if err := st.Pool().QueryRow(ctx,
		`SELECT evidence_ref FROM cnfi_signal_config WHERE key='layer_targets'`).Scan(&ref); err != nil || ref == "" {
		t.Fatalf("evidence_ref empty: %v", err)
	}
	var _ = time.Now
}
