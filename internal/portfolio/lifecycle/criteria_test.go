// criteria_test.go —— D-260 M4.2：机读判据评估器锚测。
// 核心验收=「判据表改行不改码即变淘汰行为」——本测改 Criterion 参证语义翻转。
package lifecycle

import "testing"

func metrics() map[string]float64 {
	return map[string]float64{
		"total_trades": 40, "win_rate": 0.5, "avg_pnl": 1.0, "median_pnl": 1.0,
	}
}

// TestM42_CriteriaChangeFlip 改 params 行→判定翻转（验收③：判据=数据非码）。
func TestM42_CriteriaChangeFlip(t *testing.T) {
	row := Criterion{Pred: "large_sample_kill", Params: map[string]any{
		"trades_gte": 30.0, "mode": "any", "conds": []any{"win_rate<0.3", "avg_pnl<0"}}}
	fire, _ := FireCriteria(metrics(), []Criterion{row})
	if fire {
		t.Fatal("win_rate=0.5/avg=1.0 不应点火")
	}
	// 改行：胜率线 0.3→0.6 → 同 metrics 点火。
	row.Params["conds"] = []any{"win_rate<0.6", "avg_pnl<0"}
	fire, hits := FireCriteria(metrics(), []Criterion{row})
	if !fire || len(hits) != 1 || hits[0] != "large_sample_kill" {
		t.Fatalf("改行后应点火命中 pred: fire=%v hits=%v", fire, hits)
	}
}

// TestM42_MissingMetricFailClosed cond 引用无证 metric→恒 false（不假 0 放行）。
func TestM42_MissingMetricFailClosed(t *testing.T) {
	row := Criterion{Pred: "x", Params: map[string]any{
		"mode": "any", "conds": []any{"nonexistent_metric<0"}}}
	if fire, _ := FireCriteria(metrics(), []Criterion{row}); fire {
		t.Fatal("无证 metric cond 应恒 false")
	}
}

// TestM42_RangeGates trades_lt/trades_gte 区间门语义。
func TestM42_RangeGates(t *testing.T) {
	row := Criterion{Pred: "small", Params: map[string]any{
		"trades_gte": 5.0, "trades_lt": 30.0, "mode": "all", "conds": []any{"avg_pnl>0"}}}
	if fire, _ := FireCriteria(metrics(), []Criterion{row}); fire {
		t.Fatal("total=40 越出 [5,30) 区间应不点火")
	}
	m := metrics()
	m["total_trades"] = 10
	if fire, _ := FireCriteria(m, []Criterion{row}); !fire {
		t.Fatal("total=10 在区间内+avg>0 应点火")
	}
}

// TestM42_ModeAllAny mode=all 全真才点 / any 任一即点。
func TestM42_ModeAllAny(t *testing.T) {
	base := map[string]any{"trades_gte": 1.0, "conds": []any{"win_rate<0.3", "avg_pnl>0"}}
	rowAll := Criterion{Pred: "all", Params: map[string]any{"mode": "all", "trades_gte": 1.0, "conds": base["conds"]}}
	if fire, _ := FireCriteria(metrics(), []Criterion{rowAll}); fire {
		t.Fatal("mode=all 一假即不点")
	}
	rowAny := Criterion{Pred: "any", Params: map[string]any{"mode": "any", "trades_gte": 1.0, "conds": base["conds"]}}
	if fire, _ := FireCriteria(metrics(), []Criterion{rowAny}); !fire {
		t.Fatal("mode=any avg_pnl>0 真应点")
	}
}
