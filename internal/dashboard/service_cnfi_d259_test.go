// D-259 P2 S4 断言（dashboard 面）：passes_screen state 驱动 + R8 文案保留。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/testutil/storetest"
)

// 断言：fund_underperf signal 存在 → passes_screen=false；不存在 → true.
func TestD259_PassesScreenStateDriven(t *testing.T) {
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_signal_state WHERE key LIKE 'fund_underperf_D259%'`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_signal_state WHERE key LIKE 'layer_dd_coupon'`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_nav WHERE code LIKE 'D259P%'`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_instruments WHERE code LIKE 'D259P%'`)
	})
	// 两基金同层 coupon，正常序列
	for _, c := range []struct{ code string }{{"D259P1"}, {"D259P2"}} {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO cnfi_instruments (code,kind,name,is_active,layer) VALUES ($1,'fund',$2,true,'coupon')
			 ON CONFLICT (code) DO UPDATE SET layer='coupon', is_active=true`, c.code, c.code+"-name"); err != nil {
			t.Fatalf("instrument %s: %v", c.code, err)
		}
		today := time.Now().UTC().Format("2006-01-02")
		ljjz := 1.0
		if err := st.UpsertCNFINAV(ctx, c.code, today, 1.0, &ljjz, "test", time.Now().UTC()); err != nil {
			t.Fatalf("nav %s: %v", c.code, err)
		}
	}
	// 预置 fund_underperf_D259P1 signal state（今日）→ P1 passes_screen=false
	today := time.Now().UTC().Format("2006-01-02")
	if err := st.UpsertCNFISignalState(ctx, "fund_underperf_D259P1", "observe", 25.0, 2, today, "d259-test"); err != nil {
		t.Fatalf("seed signal: %v", err)
	}

	s := &Service{store: st}
	layers, err := buildCNFICompare(ctx, s, time.Now().UTC())
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	var coupon *v1.CnfiLayerCompare
	for _, lc := range layers {
		if lc.Layer == "coupon" {
			coupon = lc
		}
	}
	if coupon == nil {
		t.Fatal("coupon layer missing")
	}
	get := func(code string) *v1.CnfiFundCompare {
		for _, f := range coupon.Funds {
			if f.Code == code {
				return f
			}
		}
		return nil
	}
	p1, p2 := get("D259P1"), get("D259P2")
	if p1 == nil || p2 == nil {
		t.Fatalf("funds missing: P1=%v P2=%v", p1, p2)
	}
	if p1.PassesScreen {
		t.Errorf("P1 passes_screen=true, want false（fund_underperf signal 存在）")
	}
	if !p2.PassesScreen {
		t.Errorf("P2 passes_screen=false, want true（无 signal）")
	}
}

// 断言：R8 文案「收益含分红，未含再投资」保留 + 「候选对比，非推荐」红线.
func TestD259_FrontendWordingRetained(t *testing.T) {
	src := d247Source(t, "web/src/components/CnfiCompareCard.tsx")
	if !strings.Contains(src, "未含再投资") {
		t.Errorf("对比卡缺 R8 口径小注「未含再投资」")
	}
	if !strings.Contains(src, "候选对比，非推荐") {
		t.Errorf("对比卡缺红线文案「候选对比，非推荐」")
	}
}
