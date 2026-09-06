// D-247 P1 断言⑰（dashboard 面：对比计算+前端文本+flag 注册）。
package dashboard

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
}

// 断言⑰e：cnfiprobe 注册 -backfill-nav 模式。
func TestD247_BackfillFlag(t *testing.T) {
	src := d247Source(t, "cmd/cnfiprobe/main.go")
	if !strings.Contains(src, "-backfill-nav") {
		t.Errorf("cnfiprobe 缺 -backfill-nav 回填模式")
	}
}
