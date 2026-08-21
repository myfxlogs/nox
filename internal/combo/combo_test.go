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

// TestKeyFromOppID_互逆 ComboKey ↔ opportunity_id 截尾互逆。
func TestKeyFromOppID_互逆(t *testing.T) {
	strategy, canonical, vBuy, vSell := "MEANREVERT", "NAS100", "REDACTED_MT_ACCT:mt5", "277842155:mt5"
	ck := Key(strategy, canonical, vBuy, vSell)
	oppID := ck + ":1787296014868"
	if FromOppID(oppID) != ck {
		t.Errorf("互逆失败: FromOppID(%q) = %q, want %q", oppID, FromOppID(oppID), ck)
	}
}

func TestMatchOppID(t *testing.T) {
	ck := Key("SWAPARB", "AUDJPY", "52993526:mt5", "277842155:mt5")
	oppID := ck + ":1787282156117"
	if !MatchOppID(oppID, ck) {
		t.Errorf("MatchOppID 应命中")
	}
	if MatchOppID(ck, ck) {
		t.Errorf("MatchOppID 长度相等应不命中（oppID 需比 comboKey 长）")
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
