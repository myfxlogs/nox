package dashboard

import "testing"

// TestAliasForVenueKey D-175：venueKey → alias 缓存查询（冒号前 venue 查 map）。
func TestAliasForVenueKey(t *testing.T) {
	s := &Service{}
	// 未接线（nil map）→ 空串
	if got := s.aliasForVenueKey("1314615:mt5"); got != "" {
		t.Errorf("nil map: got %q, want empty", got)
	}
	// 注入 alias map（key=venue，即 venueKey 冒号前部分）
	s.SetAliasMap(map[string]string{
		"1314615":          "pt1314615",
		"REDACTED_MT_ACCT": "ICR1503****",
		"binance":          "Binance",
	})
	cases := []struct {
		venueKey string
		want     string
	}{
		{"1314615:mt5", "pt1314615"},
		{"REDACTED_MT_ACCT:mt5", "ICR1503****"},
		{"1314615", "pt1314615"},    // 无冒号 → 整串当 venue
		{"unknown:mt5", ""},         // 未命中 → 空
		{"", ""},                    // 空串 → 空
		{":mt5", ""},                // 冒号在首位（idx=0 不 >0）→ 整串查 ":mt5" → 空
		{"binance:perp", "Binance"}, // venue=binance → 命中
	}
	for _, c := range cases {
		got := s.aliasForVenueKey(c.venueKey)
		if got != c.want {
			t.Errorf("aliasForVenueKey(%q): got %q, want %q", c.venueKey, got, c.want)
		}
	}
}

// TestSetAliasMap_Concurrent D-175：SetAliasMap 与 aliasForVenueKey 并发安全（RWMutex）。
func TestSetAliasMap_Concurrent(t *testing.T) {
	s := &Service{}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			s.SetAliasMap(map[string]string{"v": "alias"})
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		_ = s.aliasForVenueKey("v:mt5")
	}
	<-done
}
