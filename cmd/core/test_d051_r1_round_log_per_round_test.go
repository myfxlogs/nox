// d051r1_round_log_test.go —— D-051 R1③④锚：聚合日志「每轮一行，不按 hook 调用打」——
// note×40（hook 产出入队）零日志；step×N（轮 pass）恰 N 行聚合行。
package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"nox/internal/platform/testutil/storetest"
	"nox/internal/research/detector"
)

func TestD051R1_RoundLogPerRound(t *testing.T) {
	ctx := context.Background()
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN") // 共享池不自闭（D-351）

	r := &cexPaperRound{}
	r.setLatest([]detector.Candidate{{
		Type: detector.TypeFundingHedge, Canonical: "D51LUSDT",
		VenueBuy: "okx:spot", VenueSell: "okx:perp", GrossBps: 12.0,
	}})
	// hook 模拟：40 次入队调用（note）——零日志是③「不按 hook 调用打」的钉。
	for i := 0; i < 40; i++ {
		r.note([]string{"D51LUSDT"}, map[string]int{"funding_low": 1})
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	// 两轮 pass → 恰两行「opened=… skips={…}」聚合行；skips 累加 40 次入队。
	for i := 0; i < 2; i++ {
		r.step(ctx, st, time.Now(), nil)
	}
	lines := strings.Count(buf.String(), "paper cex carry: opened=")
	if lines != 2 {
		t.Fatalf("日志行数=轮数钉：2 轮应 2 行，got %d（out=%q）", lines, buf.String())
	}
	if !strings.Contains(buf.String(), "funding_low:40") && !strings.Contains(buf.String(), "funding_low\":40") {
		t.Fatalf("skips 聚合应含 funding_low×40，got %q", buf.String())
	}
}
