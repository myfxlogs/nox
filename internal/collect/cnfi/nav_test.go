package cnfi

import (
	"math"
	"testing"
)

// 东财 lsjz JSON 解析（契约=规格包 §1.6；字段 FSRQ/DWJZ）。
func TestNAVPollOne_ParsesAndGuardsEmptyKeys(t *testing.T) {
	// 用小型 fake store 不可行（pgstore 具体类型）——本用例只测 fetch+parse 分离面：
	// pollOne 的 JSON→行解析经 fetchFn 注入 + DSN 门控库（TestUpsert 覆盖落库语义）。
	body := []byte(`{"Data":{"LSJZList":[
		{"FSRQ":"2099-01-02","DWJZ":"1.0845"},
		{"FSRQ":"","DWJZ":"1.08"},
		{"FSRQ":"2099-01-01","DWJZ":""},
		{"FSRQ":"2099-01-03","DWJZ":"0"}
	]},"ErrCode":0}`)
	raw, err := parseNAVBody(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("valid entries = %d, want 1（空 FSRQ/空 DWJZ/非正净值全部拒绝）", len(raw))
	}
	if raw[0].date != "2099-01-02" || raw[0].nav != 1.0845 {
		t.Fatalf("entry = %+v", raw[0])
	}
}

func TestNAVPollOne_EmptyList(t *testing.T) {
	if _, err := parseNAVBody([]byte(`{"Data":{"LSJZList":[]},"ErrCode":0}`)); err == nil {
		t.Fatal("empty LSJZList must error（源空=断流信号输入，非静默）")
	}
	if _, err := parseNAVBody([]byte(`not json`)); err == nil {
		t.Fatal("non-json must error")
	}
}

// --- D-247 P1 断言⑰（先红后绿；候选池对比数据面） ---

// 断言⑰a：LJJZ 口径区间收益（含分红=DWJZ 除息跳降但 LJJZ 平滑——累计口径天然覆盖）。
func TestD247_IntervalReturn_LJJZBasis(t *testing.T) {
	pts := []LJJZPoint{
		{Date: "2026-08-01", LJJZ: 1.00},
		{Date: "2026-08-15", LJJZ: 1.05}, // DWJZ 会除息跳降，LJJZ 平滑
		{Date: "2026-08-31", LJJZ: 1.10},
	}
	got := IntervalReturn(pts)
	if got == nil || math.Abs(*got-10.0) > 1e-9 {
		t.Fatalf("interval return = %v, want 10.0%%", got)
	}
}

// 断言⑰b：最大回撤精确值（峰 1.20 谷 0.90 → 25%）。
func TestD247_MaxDrawdown(t *testing.T) {
	pts := []LJJZPoint{
		{Date: "2026-07-01", LJJZ: 1.00},
		{Date: "2026-07-15", LJJZ: 1.20},
		{Date: "2026-08-01", LJJZ: 0.90},
		{Date: "2026-08-15", LJJZ: 1.10},
	}
	got := MaxDrawdown(pts)
	if got == nil || *got != 25.0 {
		t.Fatalf("max drawdown = %v, want 25.0%%", got)
	}
}

// 断言⑰f：口径断点检测（R1 核心：1.00→1.30 跳变 30%>15% → 断点日=跳变日）。
func TestD247_DetectCaliberBreak(t *testing.T) {
	pts := []LJJZPoint{
		{Date: "2026-01-10", LJJZ: 1.00},
		{Date: "2026-01-11", LJJZ: 1.30}, // +30% 断点（转型/折算）
		{Date: "2026-01-12", LJJZ: 1.31},
	}
	day := DetectCaliberBreak(pts)
	if day == nil || day.Format("2006-01-02") != "2026-01-11" {
		t.Fatalf("break = %v, want 2026-01-11", day)
	}
	// 平滑序列无断点
	smooth := []LJJZPoint{{Date: "2026-01-10", LJJZ: 1.00}, {Date: "2026-01-11", LJJZ: 1.01}}
	if DetectCaliberBreak(smooth) != nil {
		t.Fatal("smooth series must not flag break")
	}
}
