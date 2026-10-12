// wire_scan_test.go —— D-056③ 锚：纸面轮次 ticker 首醒整点对齐（UTC
// (h mod 4)=0 的 :05，严格 >now，间隔 ≤4h）。
package main

import (
	"testing"
	"time"
)

func TestD056_PaperRoundAlign(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026-10-10T14:30:00Z", "2026-10-10T16:05:00Z"},           // 常规：下一 4h 界 :05
		{"2026-10-10T16:05:00Z", "2026-10-10T20:05:00Z"},           // 恰在界点 → 下一界（不回头）
		{"2026-10-10T16:04:59Z", "2026-10-10T16:05:00Z"},           // 界前 1s → 当界
		{"2026-10-10T23:59:00Z", "2026-10-11T00:05:00Z"},           // 跨日 00:05Z
		{"2026-10-10T00:00:00Z", "2026-10-10T00:05:00Z"},           // 00:00Z 结算后 :05 轮
		{"2026-10-10T19:07:00+08:00", "2026-10-10T20:05:00+08:00"}, // 非 UTC 入参同口径
	}
	for _, c := range cases {
		in, _ := time.Parse(time.RFC3339, c.in)
		want, _ := time.Parse(time.RFC3339, c.want)
		got := nextPaperRoundAt(in)
		if !got.Equal(want) {
			t.Fatalf("nextPaperRoundAt(%s) = %s, want %s", c.in, got, c.want)
		}
		if got.Minute() != 5 || got.Hour()%4 != 0 || !got.After(in) || got.Sub(in) > 4*time.Hour {
			t.Fatalf("对齐性质破缺: %s → %s", c.in, got)
		}
	}
}
