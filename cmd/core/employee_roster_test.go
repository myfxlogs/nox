package main

import "testing"

// TestDeptInvest_Roster 投资部实体化锚（D-595 审计偏差②）：三件 daemon
// 挂 Dept=投资——armed 评估环 pendingGateWatch / 提案扫描 oppmatch /
// 毕业环 carry-universe（研究生态的别处行不失联照跑）。
func TestDeptInvest_Roster(t *testing.T) {
	want := map[string]bool{
		"main:pendingGateWatch": false,
		"main:oppmatch":         false,
		"main:carry-universe":   false,
	}
	for _, e := range employeeRoster {
		if e.Dept == "投资" {
			if _, ok := want[e.Name]; ok {
				want[e.Name] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("投资部名册缺 %s", name)
		}
	}
}
