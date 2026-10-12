// d065_test.go —— D-065 锚：三面系统提示词契约子集关键词 + weekly 工具 spec 含 query_ro。
package explorer

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/platform/analyst"
)

// TestD065_SystemPromptContractSubset 三面提示词各含契约子集锚（证伪/触发点/口径）。
// probe_report 无工具——契约项只写与输入段引用相关的三条，不写需工具的条款。
// 三面再含 矛盾（13）/分层（15）；判别性最小实验（14）daily 无（无 query 面），
// weekly/probe 有（D-099 R1：probe 版去工具措辞「基于输入段」；probe 面永不含 query_ro）。
func TestD065_SystemPromptContractSubset(t *testing.T) {
	for name, p := range map[string]string{
		"daily":        dailySystemPrompt,
		"weekly":       weeklySystemPrompt,
		"probe_report": probeReportSystemPrompt,
	} {
		for _, kw := range []string{"证伪", "触发点", "口径", "矛盾", "分层"} {
			if !strings.Contains(p, kw) {
				t.Fatalf("%s prompt 缺契约锚 %q", name, kw)
			}
		}
	}
	if strings.Contains(dailySystemPrompt, "判别") {
		t.Fatalf("daily prompt 不应含「判别」（无 query 面，14 不写）")
	}
	if !strings.Contains(weeklySystemPrompt, "判别") {
		t.Fatalf("weekly prompt 缺契约锚 %q", "判别")
	}
	if !strings.Contains(probeReportSystemPrompt, "判别") {
		t.Fatalf("probe_report prompt 缺契约锚 %q（D-099 R1：14 无工具措辞版）", "判别")
	}
	if strings.Contains(probeReportSystemPrompt, "query_ro") {
		t.Fatalf("probe_report prompt 不应含 query_ro（该面无此工具）")
	}
}

// TestD065_WeeklySpecHasQueryRO weekly 传入 specs 必含 query_ro（D-059 实拍：
// arb_ro 断链时周深挖无自救力——spec 缺失即静默退化，锚住防回归）。
func TestD065_WeeklySpecHasQueryRO(t *testing.T) {
	var gotSpecs []analyst.ToolSpec
	capture := func(_ context.Context, _ *analyst.Client, _ analyst.ToolExecutor, specs []analyst.ToolSpec,
		_ string, _ []analyst.Message, cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
		gotSpecs = specs
		cb.OnDelta("## 观察\nx\n## 数据\ny\n## 提案线索\nz\n")
		return analyst.LoopResult{Grounded: true}, nil
	}
	dir := t.TempDir()
	deps := Deps{Store: newFakeStore(), Client: analyst.NewClient("http://x", "k", "m"),
		Run: capture, Now: func() time.Time { return time.Date(2026, 10, 4, 7, 13, 0, 0, BJTime) },
		ResearchDir: dir}
	if _, err := RunWeekly(context.Background(), deps); err != nil {
		t.Fatalf("weekly: %v", err)
	}
	for _, s := range gotSpecs {
		if s.Name == "query_ro" {
			return
		}
	}
	t.Fatalf("weekly specs 无 query_ro: %+v", gotSpecs)
}
