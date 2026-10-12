package combo

import "testing"

func TestKey(t *testing.T) {
	got := Key("SWAPARB", "AUDJPY", "52993526:mt5", "277842155:mt5")
	want := "SWAPARB:AUDJPY:52993526:mt5:277842155:mt5"
	if got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}
}

func TestFromOppID(t *testing.T) {
	oppID := "SWAPARB:AUDJPY:52993526:mt5:277842155:mt5:1787282156117"
	got := FromOppID(oppID)
	want := "SWAPARB:AUDJPY:52993526:mt5:277842155:mt5"
	if got != want {
		t.Errorf("FromOppID() = %q, want %q", got, want)
	}
}

// TestKeyFromOppID_互逆 ComboKey ↔ opportunity_id 截尾互逆（旧 tick 格式）。
func TestKeyFromOppID_互逆(t *testing.T) {
	strategy, canonical, vBuy, vSell := "MEANREVERT", "NAS100", "REDACTED_MT_ACCT:mt5", "277842155:mt5"
	ck := Key(strategy, canonical, vBuy, vSell)
	oppID := ck + ":1787296014868"
	if FromOppID(oppID) != ck {
		t.Errorf("互逆失败: FromOppID(%q) = %q, want %q", oppID, FromOppID(oppID), ck)
	}
}

// TestFromOppID_NewFormat 新组合键格式（尾段=venueType 非数字）→ 原样返回。
func TestFromOppID_NewFormat(t *testing.T) {
	ck := Key("SWAPARB", "AUDJPY", "52993526:mt5", "277842155:mt5")
	if got := FromOppID(ck); got != ck {
		t.Errorf("新格式原样返回: FromOppID(%q) = %q, want %q", ck, got, ck)
	}
}

func TestMatchOppID(t *testing.T) {
	ck := Key("SWAPARB", "AUDJPY", "52993526:mt5", "277842155:mt5")
	oppID := ck + ":1787282156117"
	if !MatchOppID(oppID, ck) {
		t.Errorf("MatchOppID 应命中（旧 tick 前缀匹配）")
	}
	// 等值命中（新持仓 oppID==组合键）
	if !MatchOppID(ck, ck) {
		t.Errorf("MatchOppID 等值应命中（新格式）")
	}
	if MatchOppID("OTHER:foo:bar:baz:123", ck) {
		t.Errorf("MatchOppID 不同前缀应不命中")
	}
}

func TestFromOppID_NoColon(t *testing.T) {
	if FromOppID("abc") != "abc" {
		t.Errorf("无冒号异常输入应原样返回")
	}
}
