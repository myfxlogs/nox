// D-236 施工：对冲卡信息分区六条源文本断言（先红后绿）。
// ①投票年化/流动/能量/带宽文本位于 hedge-acct-block 渲染段
// ②强平格 title 含「平台口径」与两净值文本
// ③质押格不含「流动」
// ④现价行含「秒前」
// ⑤App.tsx 含 cmcWatch 透传
// ⑥APR 渲染含 floor 截断（Math.floor）且无 toFixed(2) 残留于 APR 三数
package dashboard

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func hedgeStructureSource(t *testing.T) string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(testFile), "..", "..", "web", "src", "components", "PositionsPageHedgeStructure.tsx")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hedge structure component: %v", err)
	}
	return string(data)
}

// 断言①：投票年化/流动/能量/带宽文本位于 hedge-acct-block 渲染段。
func TestD236_AccountInfoInAcctBlock(t *testing.T) {
	src := hedgeStructureSource(t)
	idx := strings.Index(src, "hedge-acct-block")
	if idx < 0 {
		t.Fatal("hedge-acct-block 段不存在")
	}
	block := src[idx:]
	for _, want := range []string{"流动", "能量", "带宽", "投票年化"} {
		if !strings.Contains(block, want) {
			t.Errorf("hedge-acct-block 段缺文本 %q", want)
		}
	}
}

// 断言②：强平格 title 含「平台口径」与两净值文本（收入 title 悬停，非分散在不同 span 元素）。
func TestD236_CloseoutTitle(t *testing.T) {
	src := hedgeStructureSource(t)
	for _, want := range []string{"平台口径", "强平净值", "预警净值"} {
		if !strings.Contains(src, want) {
			t.Errorf("源文本缺 %q", want)
		}
	}
	// 改前：平台口径与强平净值分散在不同 span/br 元素中（之间有标签间隔）
	// 改后：三文本收入同一 title 字符串（之间无 span/br 标签）
	platIdx := strings.Index(src, "平台口径")
	strongIdx := strings.Index(src, "强平净值")
	if platIdx < 0 || strongIdx < 0 {
		t.Fatal("源文本缺平台口径或强平净值")
	}
	between := src[platIdx:strongIdx]
	if strings.Contains(between, "<span") || strings.Contains(between, "</span") || strings.Contains(between, "<br") {
		t.Error("平台口径与强平净值仍分散在不同元素，未收入同一 title")
	}
}

// 断言③：质押格（legSpot 区域）不含「流动」。
func TestD236_StakeCellNoLiquid(t *testing.T) {
	src := hedgeStructureSource(t)
	stakeIdx := strings.Index(src, "质押")
	blockIdx := strings.Index(src, "hedge-acct-block")
	if stakeIdx < 0 || blockIdx < 0 || stakeIdx >= blockIdx {
		t.Fatal("无法定位质押格与 hedge-acct-block 段")
	}
	legSpotRegion := src[stakeIdx:blockIdx]
	if strings.Contains(legSpotRegion, "流动") {
		t.Fatal("质押格（legSpot 区域）不应含「流动」")
	}
}

// 断言④：现价行含「秒前」。
func TestD236_PriceRowSecondsAgo(t *testing.T) {
	src := hedgeStructureSource(t)
	if !strings.Contains(src, "秒前") {
		t.Fatal("现价行缺「秒前」时戳")
	}
}

// 断言⑤：App.tsx 含 cmcWatch 透传。
func TestD236_AppCmcWatchProp(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(testFile), "..", "..", "web", "src", "App.tsx")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read App.tsx: %v", err)
	}
	if !strings.Contains(string(data), "cmcWatch") {
		t.Fatal("App.tsx 缺 cmcWatch 透传")
	}
}

// 断言⑥：APR 渲染含 floor 截断（Math.floor）且无 toFixed(2) 残留于 APR 三数。
func TestD236_APRFloorTruncation(t *testing.T) {
	src := hedgeStructureSource(t)
	if !strings.Contains(src, "Math.floor") {
		t.Fatal("APR 渲染缺 Math.floor 截断")
	}
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "投票年化") && strings.Contains(line, "toFixed(2)") {
			t.Fatalf("APR 行残留 toFixed(2): %s", strings.TrimSpace(line))
		}
	}
}
