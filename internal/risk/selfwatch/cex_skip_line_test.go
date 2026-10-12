// d051_cexskips_test.go —— D-051③锚：日报 CEX 拦截分布行——五闸因人话化渲染 +
// app_config 快照差分（首报累计、次报环比、重启回落当前值）。
package selfwatch

import (
	"context"
	"strings"
	"testing"
)

func TestD051_CexSkipLine(t *testing.T) {
	ctx := context.Background()
	cur := map[string]int{"depth_missing": 3, "funding_low": 2, "cooldown": 1, "flag": 4}
	d := Deps{
		Store:        &fakeStore{cfg: map[string]string{}},
		CexSkipStats: func() map[string]int { return cur },
	}
	line, err := d.cexSkipLine(ctx)
	if err != nil {
		t.Fatalf("cexSkipLine: %v", err)
	}
	for _, want := range []string{"纸面 CEX 开仓拦截", "深度无样本 3", "深度不足 0", "息费不足 2", "报价断流 0", "冷却中 1", "其他 4"} {
		if !strings.Contains(line, want) {
			t.Fatalf("首报分布缺 %q：%s", want, line)
		}
	}
	// D-056② 禁词复归：闸因键英文名不透出正文
	for _, ban := range []string{"depth_missing", "depth_low", "funding_low", "quote_dead"} {
		if strings.Contains(line, ban) {
			t.Fatalf("内部门禁词 %q 泄漏进日报正文：%s", ban, line)
		}
	}
	// 次报=环比（同计数 → 全零）
	line2, err := d.cexSkipLine(ctx)
	if err != nil {
		t.Fatalf("cexSkipLine 2nd: %v", err)
	}
	if !strings.Contains(line2, "深度无样本 0") || !strings.Contains(line2, "其他 0") {
		t.Fatalf("环比应为全零：%s", line2)
	}
	// 重启语义：计数回落低于基线 → 按当前值示（差分不负数）
	d.CexSkipStats = func() map[string]int { return map[string]int{"depth_missing": 1} }
	line3, err := d.cexSkipLine(ctx)
	if err != nil {
		t.Fatalf("cexSkipLine 3rd: %v", err)
	}
	if !strings.Contains(line3, "深度无样本 1") {
		t.Fatalf("重启后差分为负应按当前值：%s", line3)
	}
}
