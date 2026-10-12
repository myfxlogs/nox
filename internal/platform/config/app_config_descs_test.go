package config

import "testing"

// TestAppConfigDescs_AllNonEmpty D-526 A1：全白名单键 desc 非空闸（防漏补回归）。
func TestAppConfigDescs_AllNonEmpty(t *testing.T) {
	for _, k := range AppConfigAllKeys() {
		if appConfigDescs[k] == "" {
			t.Errorf("key %s 缺中文说明", k)
		}
	}
	// 组标签六组全覆盖
	for _, g := range []string{"AUTOTRADE", "CARRY", "EXPLORER", "ANALYST", "FUNDING", "OTHER"} {
		if AppConfigGroupLabel(g) == "" {
			t.Errorf("group %s 缺中文标签", g)
		}
	}
}
