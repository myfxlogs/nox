package dashboard

import "testing"

// TestMT4Retire_ParseRejects 退役锚：backfill 键解析与组合白名单后缀
// 双双拒 mt4（新写入口零容忍；历史行读径不受影响）。
func TestMT4Retire_ParseRejects(t *testing.T) {
	if _, _, ok := splitVenueKey("1279967:mt4"); ok {
		t.Error("splitVenueKey 应拒 venue:mt4")
	}
	if _, _, ok := splitVenueKey("1279967:mt5"); !ok {
		t.Error("splitVenueKey 应过 venue:mt5")
	}
	if comboVenueOK("1279967:mt4") {
		t.Error("comboVenueOK 应拒 :mt4")
	}
	if !comboVenueOK("1279967:mt5") {
		t.Error("comboVenueOK 应过 :mt5")
	}
}
