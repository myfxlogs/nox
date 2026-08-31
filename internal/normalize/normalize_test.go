package normalize

import "testing"

// D-140 S1 单测（先红后绿）。
// 先红=新测试在新代码上首次跑（实现已写但测试验证断言）；断言不符=红，修实现至绿。

// 用例①：规则匹配正例（≥4，含大小写/前缀变体）
func TestRuleMatchPositive(t *testing.T) {
	eng := NewRuleEngine()
	cases := []struct {
		input     string
		wantCanon string
	}{
		// 油类（大小写不敏感）
		{"UKOIL", "BRENT"},
		{"ukoil", "BRENT"},
		{"BRENT", "BRENT"},
		{"xbrUSD", "BRENT"}, // 前缀变体
		{"WTI", "WTI"},
		{"USOIL", "WTI"},
		{"uscrud", "WTI"},
		// 贵金属（D-175：只匹配 USD 计价）
		{"XAUUSD", "XAUUSD"},
		{"GOLD", "XAUUSD"},
		{"XAGUSD", "SILVER"},
		{"SILVER", "SILVER"},
		// FX 直盘
		{"EURUSD", "EURUSD"},
		{"eur.usd", "EURUSD"},
		{"EURUSD.spot", "EURUSD"}, // 后缀变体
		{"GBPJPY", "GBPJPY"},
		{"AUDJPY", "AUDJPY"},
		// 加密 CFD
		{"BTCUSD", "BTCUSDT"},
		{"btc", "BTCUSDT"},
		{"Bitcoin", "BTCUSDT"}, // 大小写不敏感
		{"ETHUSD", "ETHUSDT"},
		// 指数
		{"US30", "US30"},
		{"DJ30", "US30"},
		{"NAS100", "NAS100"},
		{"GER40", "GER40"},
		{"DAX", "GER40"},
	}
	for _, c := range cases {
		got, ok := eng.Match(c.input)
		if !ok {
			t.Errorf("Match(%q): not matched, want %s", c.input, c.wantCanon)
			continue
		}
		if got != c.wantCanon {
			t.Errorf("Match(%q): got %s, want %s", c.input, got, c.wantCanon)
		}
	}
}

// 用例②：规则匹配负例（≥4，不命中的符号）
func TestRuleMatchNegative(t *testing.T) {
	eng := NewRuleEngine()
	cases := []string{
		"UNKNOWN_SYMBOL",
		"XYZ123",
		"FOOBAR",
		"123ABC",
		"", // 空串不匹配
	}
	for _, s := range cases {
		got, ok := eng.Match(s)
		if ok {
			t.Errorf("Match(%q): matched %s, want not matched", s, got)
		}
	}
}

// 用例③：CEX USDT 直通规则（最低优先级兜底）
func TestCEXDirectPass(t *testing.T) {
	eng := NewRuleEngine()
	cases := []struct {
		input     string
		wantCanon string
	}{
		{"SOLUSDT", "SOLUSDT"},   // 直通（大写化）
		{"adausdt", "ADAUSDT"},   // 小写→大写
		{"linkusdt", "LINKUSDT"}, // 直通
	}
	for _, c := range cases {
		got, ok := eng.Match(c.input)
		if !ok {
			t.Errorf("Match(%q): not matched, want %s (CEX direct)", c.input, c.wantCanon)
			continue
		}
		if got != c.wantCanon {
			t.Errorf("Match(%q): got %s, want %s", c.input, got, c.wantCanon)
		}
	}
}

// 用例④：规则不产 CS（红线——规则库只产 canonical，CS 必须来自真实行）
func TestRuleNoCS(t *testing.T) {
	eng := NewRuleEngine()
	// Match 只返回 (canonical, ok)，无 CS 字段——编译期保证不产 CS。
	// 此测试验证 Match 签名不含 CS（类型系统已保证，此处为断言性测试）。
	got, ok := eng.Match("BRENT")
	if !ok {
		t.Fatal("Match(BRENT) should match")
	}
	if got != "BRENT" {
		t.Errorf("got %s, want BRENT", got)
	}
	// 红线断言：Match 返回值只有 canonical + bool，无 CS——签名编译期固定。
	// 若有人改 Match 返回 CS，此处编译失败=红线触发。
}

// 用例⑤：优先级——高优先规则覆盖宽泛规则
func TestRulePriority(t *testing.T) {
	eng := NewRuleEngine()
	// BTCUSD 应匹配加密规则（priority=30）而非 CEX 直通（priority=90）
	got, ok := eng.Match("BTCUSD")
	if !ok {
		t.Fatal("Match(BTCUSD) should match")
	}
	// 加密规则 → BTCUSDT（非直通的 BTCUSD）
	if got != "BTCUSDT" {
		t.Errorf("BTCUSD: got %s, want BTCUSDT (crypto rule should take priority over CEX direct)", got)
	}
	// BTCUSDT 应匹配 CEX 直通（无加密 *USDT 规则，直通兜底）
	got2, ok2 := eng.Match("BTCUSDT")
	if !ok2 {
		t.Fatal("Match(BTCUSDT) should match")
	}
	if got2 != "BTCUSDT" {
		t.Errorf("BTCUSDT: got %s, want BTCUSDT (CEX direct)", got2)
	}
}

// TestNormalize_PreciousMetalsUSDOnly D-175：贵金属 regex 只匹配 USD 计价 + 英文名，
// 不匹配跨币种合约（XAGAUD/XAGEUR/XAUXAG 等旧误归一）。
func TestNormalize_PreciousMetalsUSDOnly(t *testing.T) {
	eng := NewRuleEngine()
	// hit：USD 计价 + 英文名（含 m 后缀/数字后缀变体）
	hits := []struct {
		input     string
		wantCanon string
	}{
		{"XAGUSD", "SILVER"},
		{"XAGUSDm", "SILVER"},
		{"SILVER", "SILVER"},
		{"SILVER365", "SILVER"},
		{"XAUUSD", "XAUUSD"},
		{"XAUUSDm", "XAUUSD"},
		{"GOLD", "XAUUSD"},
		{"XPDUSD", "XPDUSD"},
		{"XPDUSDm", "XPDUSD"},
		{"XPTUSD", "XPTUSD"},
		{"XCUUSD", "XCUUSD"},
		{"XCUUSDm", "XCUUSD"},
		{"PALLADIUM", "XPDUSD"},
		{"PLATINUM", "XPTUSD"},
		{"COPPER", "XCUUSD"},
	}
	for _, c := range hits {
		got, ok := eng.Match(c.input)
		if !ok {
			t.Errorf("Match(%q): not matched, want %s (D-175 USD-only)", c.input, c.wantCanon)
			continue
		}
		if got != c.wantCanon {
			t.Errorf("Match(%q): got %s, want %s", c.input, got, c.wantCanon)
		}
	}
	// miss：非 USD 计价贵金属（旧规则误归一，新规则正确拒绝）
	misses := []string{
		"XAGAUD",
		"XAGEUR",
		"XAGGBP",
		"XAGJPY",
		"XAGAUDm",
		"XAUAUD",
		"XAUXAG",
		"XAUEUR",
		"XAUGBP",
		"XAUJPY",
		"XAGCAD",
		"XAGCHF",
		"XAGNZD",
	}
	for _, s := range misses {
		got, ok := eng.Match(s)
		if ok {
			t.Errorf("Match(%q): matched %s, want NOT matched (D-175 cross-currency reject)", s, got)
		}
	}
}
