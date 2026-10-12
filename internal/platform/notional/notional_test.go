package notional

import (
	"math"
	"testing"
)

// TestVenueAccount venue 键归一（D-087 §3.1）。
// 仅 mt4/mt5 后缀剥去；CEX 键原样；无后缀原样。
func TestVenueAccount(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"277842155:mt5", "277842155"},
		{"1314615:mt4", "1314615"},
		{"binance:spot", "binance:spot"},               // CEX 键原样
		{"okx:perp", "okx:perp"},                       // CEX 键原样
		{"52993526", "52993526"},                       // 无后缀原样
		{"", ""},                                       // 空串
		{"277842155:mt5:extra", "277842155:mt5:extra"}, // 非 :mt4/:mt5 后缀=原样
	}
	for _, c := range cases {
		if got := VenueAccount(c.in); got != c.want {
			t.Errorf("VenueAccount(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestQuoteCcy 计价币推导（D-087 §3.1，返修包②修正）。
// len==6 两段全字母+后段∈已知计价币集→后段；USD/USDT 后缀→USD；其余→""（USD 假定）。
func TestQuoteCcy(t *testing.T) {
	cases := []struct {
		canonical, want string
	}{
		// FX 六字两段全字母+后段∈已知集
		{"AUDJPY", "JPY"}, // 交叉盘
		{"EURUSD", "USD"}, // 直盘 USD-quote
		{"GBPJPY", "JPY"}, // 交叉盘
		{"GBPAUD", "AUD"}, // 交叉盘（返修包②新增）
		{"USDJPY", "JPY"}, // 直盘 USD-base
		{"USDCHF", "CHF"}, // 直盘 USD-base
		// USD/USDT 后缀
		{"XAUUSD", "USD"},  // 含 USD 后缀
		{"BTCUSDT", "USD"}, // 含 USDT 后缀
		{"ETHUSDT", "USD"}, // 含 USDT 后缀
		// 指数/商品 CFD → ""（USD 假定，fx=1）
		{"NAS100", ""}, // 返修包②：原误判 "100"
		{"SPX500", ""}, // 返修包②：原误判 "500"
		{"US30", ""},
		{"UKOIL", ""},
		{"BRENT", ""},
		// 边界
		{"", ""},        // 空串
		{"AUDJPYX", ""}, // 非 6 字非 USD 后缀
		{"ABC123", ""},  // 6 字但后段非全字母
		{"ABCXYZ", ""},  // 6 字全字母但后段∉已知集
	}
	for _, c := range cases {
		if got := QuoteCcy(c.canonical); got != c.want {
			t.Errorf("QuoteCcy(%q) = %q, want %q", c.canonical, got, c.want)
		}
	}
}

// TestLegUSD 真实规格表 4 case（D-087 §5 验收门槛）。
//   - XAU CS=100@4500 → $450K/手
//   - AUDJPY CS=100000@113.5 + USDJPY=150 → ≈$75.7K
//   - UKOIL CS=1000@92 → $92K（纯函数 fx=1 直传，QuoteCcy="" 的集成路径另案）
//   - BTCUSDT CS=1@50000 → $50K
func TestLegUSD(t *testing.T) {
	cases := []struct {
		name               string
		qty, cs, price, fx float64
		want               float64
	}{
		{"XAU 1手 CS=100 @4500 fx=1", 1, 100, 4500, 1, 450000},
		{"AUDJPY 1手 CS=100000 @113.5 fx=1/150", 1, 100000, 113.5, 1.0 / 150.0, 75666.67},
		{"UKOIL 1手 CS=1000 @92 fx=1", 1, 1000, 92, 1, 92000},
		{"BTCUSDT 1手 CS=1 @50000 fx=1", 1, 1, 50000, 1, 50000},
	}
	for _, c := range cases {
		got := LegUSD(c.qty, c.cs, c.price, c.fx)
		if math.Abs(got-c.want) > c.want*0.001 { // 0.1% 容差（AUDJPY 浮点）
			t.Errorf("%s: LegUSD = %.2f, want %.2f", c.name, got, c.want)
		}
	}
}

// TestLegUSD_ZeroInputs 任一因子 ≤0 → 0（诚实缺省，防伪造）。
func TestLegUSD_ZeroInputs(t *testing.T) {
	cases := []struct {
		name               string
		qty, cs, price, fx float64
	}{
		{"qty=0", 0, 100, 100, 1},
		{"cs=0", 1, 0, 100, 1},
		{"price=0", 1, 100, 0, 1},
		{"fx=0", 1, 100, 100, 0},
		{"qty<0", -1, 100, 100, 1},
		{"fx<0", 1, 100, 100, -1},
	}
	for _, c := range cases {
		if got := LegUSD(c.qty, c.cs, c.price, c.fx); got != 0 {
			t.Errorf("%s: LegUSD = %.2f, want 0", c.name, got)
		}
	}
}
