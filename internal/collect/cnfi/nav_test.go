package cnfi

import "testing"

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
