package exchange

import "strconv"

// f64z 交易所字符串数值解析（D-508 S2b）：""=字段缺席→0；非空脏串 error 上抛——
// 监控/采集取数解析失败=该 venue 本轮无数据，fail-closed 不进快照（同 S2 exec 域语义）。
func f64z(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}
