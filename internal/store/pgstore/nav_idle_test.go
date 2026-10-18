package pgstore

// nav_idle_test.go —— 眠钱口径锚（STATE「眠钱口径修正+瓦片换面」票·行220）：
// 桶 Idle=Σ Idle 旗池余额——earn/在产池（income30d>0 旗消）不计闲置；
// USD 桶全闲口径不动（D-286 眠钱=待部署缺口非「非持仓现金」）。
import (
	"testing"
	"time"
)

func TestSumNav_IdleFlagMetric(t *testing.T) {
	r := SumNav([]ExplorerIdlePool{
		{Venue: "binance", VenueType: "earn", Currency: "USDT", Balance: 2000, Equity: 2000}, // 在产（income30d>0→Idle=false）
		{Venue: "binance", VenueType: "spot", Currency: "USDT", Balance: 500, Equity: 500, Idle: true},
		{Venue: "gate", VenueType: "earn", Currency: "USDT", Balance: 2000, Equity: 2000}, // 在产
		{Venue: "mtacct", VenueType: "mt5", Currency: "USD", Balance: 5247, Equity: 5247, Idle: true},
	}, time.Now())
	if got := r.ByCcy["USDT"].Idle; got != 500 {
		t.Fatalf("USDT 桶 Idle=%v want 500（在产池不入闲）", got)
	}
	if got := r.ByCcy["USDT"].Nav; got != 4500 {
		t.Fatalf("USDT 桶 Nav=%v want 4500（Nav 口径不动全池入）", got)
	}
	if r.IdleUSD != 5247 {
		t.Fatalf("USD 桶全闲 IdleUSD=%v want 5247", r.IdleUSD)
	}
}
