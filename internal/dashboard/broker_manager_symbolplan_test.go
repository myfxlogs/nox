package dashboard

import (
	"testing"
)

// TestClassifySymbols_CSThreshold D-177 S1：CS 校验从精确匹配改为比值阈值 2.0 + cent 跳过。
func TestClassifySymbols_CSThreshold(t *testing.T) {
	idx := canonicalIdx{
		aliases: map[string]string{
			"CLNE":      "WTI",
			"XAGUSDpro": "SILVER",
			"XAGUSDm":   "SILVER",
			"XAGUSD":    "SILVER",
			"EURUSD":    "EURUSD",
		},
		contract: map[string]float64{
			"WTI":    1000,
			"SILVER": 5000,
			"EURUSD": 1500,
		},
	}
	cases := []struct {
		name   string
		symbol string
		cs     float64
		want   mappingStatus
	}{
		{"跨品种 CS=1 vs exp=1000", "CLNE", 1, StatusConflict},
		{"同品种变体非 cent CS=50 vs exp=5000", "XAGUSDpro", 50, StatusConflict},
		{"cent 账号 CS=50 vs exp=5000 跳过", "XAGUSDm", 50, StatusAuto},
		{"合约微差 CS=1000 vs exp=1500 ratio=1.5", "EURUSD", 1000, StatusAuto},
		{"CS 未知 CS=0", "XAGUSD", 0, StatusAuto},
		{"exp 未知 exp=0", "XAGUSD", 5000, StatusAuto},
		{"精确匹配 CS=5000 exp=5000", "XAGUSD", 5000, StatusAuto},
	}
	// exp 未知：需要单独 idx（contract[canon]=0）
	idxNoExp := canonicalIdx{
		aliases:  idx.aliases,
		contract: map[string]float64{"WTI": 1000, "SILVER": 0, "EURUSD": 1500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			useIdx := idx
			if c.symbol == "XAGUSD" && c.cs == 5000 && c.want == StatusAuto && c.name == "exp 未知 exp=0" {
				useIdx = idxNoExp
			}
			groups := []SymbolGroup{{Symbol: c.symbol, ContractSize: c.cs}}
			sugs := classifySymbols(groups, useIdx)
			if len(sugs) != 1 {
				t.Fatalf("got %d suggestions, want 1", len(sugs))
			}
			if sugs[0].Status != c.want {
				t.Errorf("status: got %s, want %s (reason=%s)", sugs[0].Status, c.want, sugs[0].Reason)
			}
		})
	}
}
