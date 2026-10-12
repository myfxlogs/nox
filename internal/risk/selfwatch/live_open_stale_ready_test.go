// d102_stage1_live_test.go —— D-102 锚：真金在仓（environment='live'）→ Stage 1 段
// 转「在仓（已启动）」、就绪红绿九项停显（含「待决策：Stage 1 可启」推送源）、
// Stage 2 段照常；计数读败 fail-closed 整报不出。
package selfwatch

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestD102_LiveOpenStaleReady 有真金在仓：段标题改「在仓（已启动）」，
// 零「就绪」红绿行（就绪九项标签+可启行全不出现）；Stage 2 段不动。
func TestD102_LiveOpenStaleReady(t *testing.T) {
	st := greenStore()
	st.liveOpen = 1
	rep, err := BuildDaily(context.Background(), testDeps(st, goodEval(), tNow))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"■ Stage 1 在仓（已启动）", "真金在仓 1 仓——就绪判据停显，平仓后恢复",
		"■ Stage 2 条件", "近 30 天真金账本",
	} {
		if !strings.Contains(rep.Body, want) {
			t.Fatalf("body missing %q:\n%s", want, rep.Body)
		}
	}
	// 锚=有 OPEN 仓时零「就绪」红绿行：九项标签+就绪标题+可推行全禁。
	for _, banned := range []string{
		"■ Stage 1 就绪", "待决策：Stage 1 可启",
		"窗内净额非负", "目标组合纸面行存活", "开仓时点", "期间零事故仓",
		"息差数据在线", "纸面计息正常", "数据库版本", "风控总闸在位",
		"仓位上限已定", "白名单已定",
	} {
		if strings.Contains(rep.Body, banned) {
			t.Fatalf("在仓态不应出现就绪行 %q:\n%s", banned, rep.Body)
		}
	}
	if rep.Stage1OK {
		t.Fatal("在仓态 Stage1OK 应为 false（空表）")
	}
}

// TestD102_LiveOpenHourlyEval D-071 小时级评估同轨：在仓 → 空表+allOK=false
// （emitStage1Ready 不再推「可启」，就绪态回落）。
func TestD102_LiveOpenHourlyEval(t *testing.T) {
	st := greenStore()
	st.liveOpen = 2
	rows, _, _, _, n, err := evalStage1(context.Background(), testDeps(st, goodEval(), tNow), tNow)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(rows) != 0 || allOK(rows) {
		t.Fatalf("在仓应返空表+计数 2：n=%d rows=%v allOK=%v", n, rows, allOK(rows))
	}
}

// TestD102_LiveOpenQueryFailClosed 在仓计数读败：整报不出（不敢证明「无在仓」
// 就不得显就绪面——否则在仓窗漏出「就绪」标题即回到误导态）。
func TestD102_LiveOpenQueryFailClosed(t *testing.T) {
	st := greenStore()
	st.liveOpenErr = errors.New("pg boom")
	if _, err := BuildDaily(context.Background(), testDeps(st, goodEval(), tNow)); err == nil {
		t.Fatal("在仓计数读败应 fail-closed 报错")
	}
	_, _, _, _, _, err := evalStage1(context.Background(), testDeps(st, goodEval(), tNow), tNow)
	if err == nil || !strings.Contains(err.Error(), "stage1 live positions") {
		t.Fatalf("evalStage1 应带在仓计数错误源: %v", err)
	}
}
