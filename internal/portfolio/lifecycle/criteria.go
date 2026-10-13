// criteria.go —— D-260 M4.2：机读判据评估器（判据=fin_strategy_criteria 行，
// 参数 jsonb 表达式，零源码字面量）。
//
// 行 params 形：
//
//	{"trades_gte":5,"trades_lt":30,"mode":"all","conds":["median_pnl<0","avg_pnl<0"]}
//
// 区间门：trades_gte/trades_lt/min_trades/max_trades/scans_gte 对照
// metrics["total_trades"]/metrics["scans_survived"]；conds 为 "metric<op>num"
// 谓词（< <= > >= ==）。mode=all 全真点火 / any 任一真点火。
// metric 缺（调用方无证可算）→ 该 cond 恒 false——fail-closed 不假设 0。
package lifecycle

import (
	"regexp"
	"strconv"
	"strings"
)

// Criterion 判据行（pgstore.StrategyCriteria 同名投影——adapter 层逐字段映射）。
type Criterion struct {
	Strategy string
	Kind     string
	Pred     string
	Params   map[string]any
}

var condRe = regexp.MustCompile(`^([a-z_]+)\s*(<=|>=|==|<|>)\s*(-?[0-9]+(?:\.[0-9]+)?)$`)

// FireCriteria 逐行判定，返回点火行 pred 名（命中痕→hit_rules）。
func FireCriteria(metrics map[string]float64, crits []Criterion) (bool, []string) {
	var hits []string
	for _, c := range crits {
		if fireOne(metrics, c) {
			hits = append(hits, c.Pred)
		}
	}
	return len(hits) > 0, hits
}

func fireOne(metrics map[string]float64, c Criterion) bool {
	trades, hasT := metrics["total_trades"]
	scans, hasS := metrics["scans_survived"]
	// 区间门：键存→metric 缺（源不在）或越界=不点火。
	if !boundOK(c.Params, "trades_gte", hasT, trades, true) ||
		!boundOK(c.Params, "min_trades", hasT, trades, true) ||
		!boundOK(c.Params, "trades_lt", hasT, trades, false) ||
		!boundOK(c.Params, "max_trades", hasT, trades, false) ||
		!boundOK(c.Params, "scans_gte", hasS, scans, true) {
		return false
	}
	conds, _ := c.Params["conds"].([]any)
	if len(conds) == 0 {
		return false
	}
	mode, _ := c.Params["mode"].(string)
	anyHit := false
	for _, raw := range conds {
		expr, _ := raw.(string)
		if evalCond(metrics, expr) {
			anyHit = true
		} else if mode == "all" {
			return false
		}
	}
	return mode == "all" || anyHit
}

// boundOK 区间门：gte=true 表下界（v>=n）/false 表上界（v<n）。
// 键缺=无约束 true；键存而 metric 缺=false（fail-closed）。
func boundOK(p map[string]any, key string, has bool, v float64, gte bool) bool {
	raw, exists := p[key]
	if !exists {
		return true
	}
	n, ok := raw.(float64)
	if !ok || !has {
		return false
	}
	if gte {
		return v >= n
	}
	return v < n
}

func evalCond(metrics map[string]float64, expr string) bool {
	m := condRe.FindStringSubmatch(strings.TrimSpace(expr))
	if m == nil {
		return false
	}
	v, has := metrics[m[1]]
	if !has {
		return false // metric 无证=恒 false（不假 0 不放行）
	}
	n, err := strconv.ParseFloat(m[3], 64)
	if err != nil {
		return false
	}
	switch m[2] {
	case "<":
		return v < n
	case "<=":
		return v <= n
	case ">":
		return v > n
	case ">=":
		return v >= n
	default:
		return v == n
	}
}
