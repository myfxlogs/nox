package dashboard

import (
	"strings"
	"testing"
)

// TestFundingWarning 纯函数表驱动（设计 §11.1 / §11.6）。全部中性通用值（D-047）。
func TestFundingWarning(t *testing.T) {
	cases := []struct {
		name      string
		current   *float64
		prev      *float64
		dropRatio float64
		want      string
	}{
		{
			name: "R1 翻负", current: f64(-0.10), prev: f64(1.00), dropRatio: 0.5,
			want: "资金费已翻负，套息变倒贴（今日 −0.10 U）",
		},
		{
			name: "R2 急降", current: f64(0.30), prev: f64(1.00), dropRatio: 0.5,
			want: "资金费急降（前日 1.00 → 今日 0.30 U），接近翻负",
		},
		{
			name: "同现取 R1（翻负优先，不报急降）", current: f64(-0.10), prev: f64(1.00), dropRatio: 0.5,
			want: "资金费已翻负，套息变倒贴（今日 −0.10 U）",
		},
		{
			name: "首条无 prev 负值只走 R1", current: f64(-0.10), prev: nil, dropRatio: 0.5,
			want: "资金费已翻负，套息变倒贴（今日 −0.10 U）",
		},
		{
			name: "首条无 prev 正值无 R2", current: f64(0.30), prev: nil, dropRatio: 0.5,
			want: "",
		},
		{
			name: "边界 ==dropRatio×prev 不触发", current: f64(0.50), prev: f64(1.00), dropRatio: 0.5,
			want: "",
		},
		{
			name: "prev=0 免疫 R2", current: f64(0.30), prev: f64(0.0), dropRatio: 0.5,
			want: "",
		},
		{
			name: "prev<0 免疫 R2", current: f64(0.30), prev: f64(-1.00), dropRatio: 0.5,
			want: "",
		},
		{
			name: "ratio=0 兜底 0.5 触发 R2", current: f64(0.30), prev: f64(1.00), dropRatio: 0,
			want: "资金费急降（前日 1.00 → 今日 0.30 U），接近翻负",
		},
		{
			name: "ratio<0 兜底 0.5 触发 R2", current: f64(0.30), prev: f64(1.00), dropRatio: -1,
			want: "资金费急降（前日 1.00 → 今日 0.30 U），接近翻负",
		},
		{
			name: "正常正值无预警", current: f64(1.00), prev: f64(1.00), dropRatio: 0.5,
			want: "",
		},
		{
			name: "current=nil 无预警", current: nil, prev: f64(1.00), dropRatio: 0.5,
			want: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fundingWarning(c.current, c.prev, c.dropRatio)
			if got != c.want {
				t.Errorf("fundingWarning(%v,%v,%.2f) want %q, got %q", c.current, c.prev, c.dropRatio, c.want, got)
			}
		})
	}

	// 同现取 R1 的附加断言：R1 命中时文案不得含「急降」（优先级 R1 > R2）。
	if got := fundingWarning(f64(-0.10), f64(1.00), 0.5); strings.Contains(got, "急降") {
		t.Errorf("R1 hit should not also report 急降, got %q", got)
	}
}

// f64 float 指针字面量（原 personal_ledger_test.go 内共享 helper——该文件 D-063 随
// personalPositionToProto 测试拆除，本文件独立持有）。
func f64(f float64) *float64 { return &f }
