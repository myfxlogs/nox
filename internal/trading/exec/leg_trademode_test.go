// leg_trademode_test.go —— D-004 P1#2：执行时 trade_mode 即时探针判定矩阵。
// 开仓 fail-closed（err/未接线/不可交易模式→拒开）；平仓减风险（err 放行，
// 仅 DISABLED 否决）；CEX 腿跳过探针。
package exec

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"arb/internal/data/fact"
)

func TestCheckTradeMode_Matrix(t *testing.T) {
	ctx := context.Background()
	bright := func() *d495FakeStore { return &d495FakeStore{changed: true, n: 3} }

	cases := []struct {
		name    string
		prober  TradeModeProber
		venue   string
		side    string
		closing bool
		wantOK  bool
		wantSub string // reason 子串；wantOK=true 时忽略
	}{
		// 开仓 fail-closed
		{"开仓_探针err拒开", &d495FakeProber{err: fmt.Errorf("rpc down")}, "3001157:mt5", "BUY", false, false, "trade_mode unknown"},
		{"开仓_Disabled拒", &d495FakeProber{mode: fact.TradeModeDisabled}, "3001157:mt5", "BUY", false, false, "disabled"},
		{"开仓_CloseOnly拒", &d495FakeProber{mode: fact.TradeModeCloseOnly}, "3001157:mt5", "BUY", false, false, "close_only"},
		{"开仓_LongOnly卖拒", &d495FakeProber{mode: fact.TradeModeLongOnly}, "3001157:mt5", "SELL", false, false, "long_only"},
		{"开仓_ShortOnly买拒", &d495FakeProber{mode: fact.TradeModeShortOnly}, "3001157:mt5", "BUY", false, false, "short_only"},
		{"开仓_LongOnly买放行", &d495FakeProber{mode: fact.TradeModeLongOnly}, "3001157:mt5", "BUY", false, true, ""},
		{"开仓_ShortOnly卖放行", &d495FakeProber{mode: fact.TradeModeShortOnly}, "3001157:mt5", "SELL", false, true, ""},
		{"开仓_Full放行", &d495FakeProber{mode: fact.TradeModeFull}, "3001157:mt5", "SELL", false, true, ""},
		{"开仓_prober未接线拒", nil, "3001157:mt5", "BUY", false, false, "prober not wired"},
		// 平仓减风险：err/未接线放行（venue 拒单为最终权威），DISABLED 仍否决
		{"平仓_探针err放行", &d495FakeProber{err: fmt.Errorf("rpc down")}, "3001157:mt5", "BUY", true, true, ""},
		{"平仓_prober未接线放行", nil, "3001157:mt5", "BUY", true, true, ""},
		{"平仓_Disabled拒", &d495FakeProber{mode: fact.TradeModeDisabled}, "3001157:mt5", "BUY", true, false, "disabled"},
		{"平仓_CloseOnly放行", &d495FakeProber{mode: fact.TradeModeCloseOnly}, "3001157:mt5", "BUY", true, true, ""},
		{"平仓_LongOnly卖放行", &d495FakeProber{mode: fact.TradeModeLongOnly}, "3001157:mt5", "SELL", true, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, why := LegActionable(ctx, bright(), tc.prober, tc.venue, "XAUUSDm", tc.side, tc.closing)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v why=%q, want ok=%v", ok, why, tc.wantOK)
			}
			if !tc.wantOK && !strings.Contains(why, tc.wantSub) {
				t.Fatalf("reason %q 应含 %q", why, tc.wantSub)
			}
		})
	}

	// CEX 腿：prober=nil 不受 trade_mode 判定影响
	if ok, why := LegActionable(ctx, bright(), nil, "okx:perp", "XAU-USDT-SWAP", "SELL", false); !ok {
		t.Fatalf("CEX 腿应跳过 trade_mode 判定: %s", why)
	}
}
