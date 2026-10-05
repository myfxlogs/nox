// d071_stage1_hourly_test.go —— D-071 C 锚：anomalyLoop 小时级 Stage 1 就绪评估。
// 全 ✅ 首评 → emitStage1Ready InsertAlertDedup 恰一次（cfgStage1Ready 翻 "1"）；
// 下一整点同态 ✅→✅ 不重复发；evalStage1 纯读面零副作用（不写 ARB_SELFWATCH_PAPER_NET
// 逐日快照——那是 BuildDaily 渲染路径的日报副作用）。
package selfwatch

import (
	"context"
	"strings"
	"testing"
)

func TestD071_HourlyStage1ReadyOnce(t *testing.T) {
	st := greenStore()
	d := testDeps(st, goodEval(), tNow)
	hourly := func() { // 镜像 anomalyLoop 唤醒体内的小时级评估段
		rows, _, _, _, err := evalStage1(context.Background(), d, d.now())
		if err != nil {
			t.Fatalf("evalStage1: %v", err)
		}
		emitStage1Ready(context.Background(), d, allOK(rows))
	}
	countReady := func() int {
		n := 0
		for _, c := range st.dedupCalls {
			if strings.Contains(c, "stage1_ready") {
				n++
			}
		}
		return n
	}

	hourly() // 全 ✅ 首评：❌→✅ 转换发一次
	if n := countReady(); n != 1 || st.upserts[cfgStage1Ready] != "1" {
		t.Fatalf("首评应 InsertAlertDedup 恰一次+态翻 1: calls=%d state=%q", n, st.upserts[cfgStage1Ready])
	}
	// 纯读面断言：小时评不得写逐日净额快照（日报副作用仅限 BuildDaily）。
	if _, wrote := st.upserts[cfgPaperNetHist]; wrote {
		t.Fatal("小时级就绪评估不得写 ARB_SELFWATCH_PAPER_NET")
	}
	hourly() // 下一整点同态 ✅→✅：不重复发
	if n := countReady(); n != 1 {
		t.Fatalf("连续 ✅ 不应重复发: total calls=%d want 1", n)
	}
}
