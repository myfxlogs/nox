// D-247 P1 断言⑰（dashboard 面：对比计算+前端文本+flag 注册）。
package dashboard

import (
	"connectrpc.com/connect"
	"context"

	"arb/internal/testutil/storetest"
	"path/filepath"

	v1 "arb/internal/gen/dashboardv1"
	"math"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func d247Source(t *testing.T, rel string) string {
	_, testFile, _, _ := runtime.Caller(0)
	return readFile(t, filepath.Join(filepath.Dir(testFile), "..", "..", rel))
}

// 断言⑰c：层内排名含 rank/N。
func TestD247_RankWithN(t *testing.T) {
	funds := []struct{ ret float64 }{{3.1}, {1.2}, {2.5}}
	ranked := RankByReturnDesc(funds, func(i int) float64 { return funds[i].ret })
	// 降序：ret 3.1→rank1, 2.5→rank2, 1.2→rank3（ranked 按原下标索引）
	if ranked[0].Rank != 1 || ranked[1].Rank != 3 || ranked[2].Rank != 2 || ranked[0].N != 3 {
		t.Fatalf("rank/N = %+v", ranked)
	}
}

// 断言⑰d：前端含「候选对比，非推荐」+「未含再投资」口径注。
func TestD247_FrontendCompareCard(t *testing.T) {
	src := d247Source(t, "web/src/components/CnfiCompareCard.tsx")
	if !strings.Contains(src, "候选对比，非推荐") {
		t.Errorf("对比卡缺「候选对比，非推荐」红线文案")
	}
	if !strings.Contains(src, "未含再投资") {
		t.Errorf("对比卡缺口径小注（未含再投资）")
	}
	if !strings.Contains(src, `passesScreen ? " ✓"`) {
		t.Errorf("对比卡缺 ✓ 筛选标记渲染（R-F4）")
	}
}

// 断言⑰e：cnfiprobe 注册 -backfill-nav 模式。
func TestD247_BackfillFlag(t *testing.T) {
	src := d247Source(t, "cmd/cnfiprobe/main.go")
	if !strings.Contains(src, "-backfill-nav") {
		t.Errorf("cnfiprobe 缺 -backfill-nav 回填模式")
	}
}

// 断言⑰f 消费侧（R-F2，D-247 复审 §4）：跨断点区间=缺失非毒值 + is_break 标记 + N=参与数。
// 同层两基金：A 正常序列（ret_3m 可得）；B 断点跨窗（ret 缺失）。B 的 N 断言在 R-F1 修复前必须红。
func TestD247_BreakCrossIntervalMissing(t *testing.T) {
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_nav WHERE code LIKE 'D247%'`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_instruments WHERE code LIKE 'D247%'`)
	})
	for _, c := range []struct{ code, name string }{
		{"D247A", "正常基金"}, {"D247B", "断点基金"},
	} {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO cnfi_instruments (code,kind,name,is_active,layer) VALUES ($1,'fund',$2,true,'coupon')
			 ON CONFLICT (code) DO UPDATE SET layer=EXCLUDED.layer`, c.code, c.name); err != nil {
			t.Fatalf("instrument: %v", err)
		}
	}
	if _, err := st.Pool().Exec(ctx,
		`UPDATE cnfi_instruments SET caliber_break_date='2026-08-20' WHERE code='D247B'`); err != nil {
		t.Fatalf("break date: %v", err)
	}
	// A：正常序列（08-01=1.00 → 09-01=1.05，ret_3m=5%）
	for _, d := range []struct{ day, ljjz string }{
		{"2026-08-01", "1.00"}, {"2026-09-01", "1.05"},
	} {
		if err := st.UpsertCNFINAV(ctx, "D247A", d.day, 1.0, strPtrD247(d.ljjz), "test", time.Now().UTC()); err != nil {
			t.Fatalf("nav A: %v", err)
		}
	}
	// B：跨断点序列（朴素计算=+31% 毒值；R1 防御=区间缺失）
	for _, d := range []struct{ day, ljjz string }{
		{"2026-08-01", "1.00"}, {"2026-08-25", "1.30"}, {"2026-09-01", "1.31"},
	} {
		if err := st.UpsertCNFINAV(ctx, "D247B", d.day, 1.0, strPtrD247(d.ljjz), "test", time.Now().UTC()); err != nil {
			t.Fatalf("nav B: %v", err)
		}
	}
	s := &Service{store: st}
	resp, err := s.GetCNFIProgress(ctx, connect.NewRequest(&v1.GetCNFIProgressRequest{}))
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	var coupon *v1.CnfiLayerCompare
	for _, lc := range resp.Msg.LayerCompares {
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
	a, b := get("D247A"), get("D247B")
	if a == nil || b == nil {
		t.Fatalf("sentinel funds missing: A=%v B=%v", a, b)
	}
	// B：跨断点 → ret 缺失（非毒值）+ is_break=true
	if b.Ret_3M != nil || b.Ret_1Y != nil {
		t.Fatalf("B 跨断点区间应缺失，got ret_3m=%v ret_1y=%v（毒值）", b.Ret_3M, b.Ret_1Y)
	}
	if b.IsBreak == nil || !*b.IsBreak {
		t.Fatalf("B is_break 应为 true")
	}
	// A：ret_3m=5% + rank=1
	if a.Ret_3M == nil || math.Abs(*a.Ret_3M-5.0) > 5.0*1e-9 {
		t.Fatalf("A ret_3m = %v, want 5.0", a.Ret_3M)
	}
	if a.Rank == nil || *a.Rank != 1 {
		t.Fatalf("A rank = %v, want 1", a.Rank)
	}
	// R-F1 核心：N=参与数（A 可得、B 缺失 → N=1；修复前 N=2=红）
	if a.N == nil || *a.N != 1 {
		t.Fatalf("A N = %v, want 1（R-F1：缺失指标不计入 N）", a.N)
	}
}

func strPtrD247(s string) *float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		panic(err)
	}
	return &v
}
