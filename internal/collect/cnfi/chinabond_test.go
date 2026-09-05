package cnfi

import (
	"os"
	"testing"
)

// golden fixture = 活体取证存档（tools/cnfi/fixtures/2026-09-05，SHA256SUMS 校验）。
const goldenFixture = "testdata/chinabond_historyQuery_2026-08-29_2026-09-05.html"

func TestParseHistoryHTML_GoldenFixture(t *testing.T) {
	raw, err := os.ReadFile(goldenFixture)
	if err != nil {
		t.Fatalf("read fixture: %v (fixtures 须随仓入库)", err)
	}
	points, warns, err := ParseHistoryHTML(string(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	// 3 曲线 × 5 业务日(08-31..09-04) × 6 tenor − 中短票 30 年空值 5 格 = 85
	if len(points) != 85 {
		t.Fatalf("points = %d, want 85", len(points))
	}
	// 规格包 §1.3 解析断言：cgb@2026-09-04 10年 = 1.6804% → 168.04bp
	found := false
	for _, p := range points {
		if p.Series == "cgb" && p.AsOf == "2026-09-04" && p.Tenor == "10y" {
			found = true
			if p.ValueBP != 168.04 {
				t.Errorf("cgb 10y = %v, want 168.04", p.ValueBP)
			}
		}
	}
	if !found {
		t.Fatal("cgb 2026-09-04 10y point missing")
	}
	// bank_aaa 3y@2026-09-04 = 1.5547% → 155.47bp
	for _, p := range points {
		if p.Series == "bank_aaa" && p.AsOf == "2026-09-04" && p.Tenor == "3y" && p.ValueBP != 155.47 {
			t.Errorf("bank_aaa 3y = %v, want 155.47", p.ValueBP)
		}
	}
	// 中短票 30 年为空 → ig_aaa 无 30y 点（missing 语义）
	for _, p := range points {
		if p.Series == "ig_aaa" && p.Tenor == "30y" {
			t.Errorf("ig_aaa 30y should be absent (empty cell), got %v", p.ValueBP)
		}
	}
	// 6月/7年 列不写库
	for _, p := range points {
		if p.Tenor == "6月" || p.Tenor == "7年" || p.Tenor == "2y" {
			t.Errorf("unmapped tenor leaked: %s", p.Tenor)
		}
	}
}

func TestParseHistoryHTML_NoHeader(t *testing.T) {
	if _, _, err := ParseHistoryHTML("<html><body>403错误</body></html>"); err == nil {
		t.Fatal("blocked/changed page must error loudly, not yield empty points")
	}
}

func TestParseHistoryHTML_EmptyCellSkipped(t *testing.T) {
	html := `<table>
<tr><td>曲线名称</td><td>日期</td><td>3月</td><td>1年</td><td>30年</td></tr>
<tr><td>中债国债收益率曲线</td><td>2026-09-04</td><td>1.2095</td><td>1.2337</td><td></td></tr>
</table>`
	points, _, err := ParseHistoryHTML(html)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("points = %d, want 2 (empty 30y skipped)", len(points))
	}
}
