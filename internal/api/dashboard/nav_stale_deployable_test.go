// nav_stale_deployable_test.go —— 总览瓦片审计②③锚：
// ②navStaleNote 陈值池计数/金额/清单（Stale 旗由 SumNav 打——FetchedAt>36h）；
// ③poolDeployableUSD 可动用口径=MT freeMargin / CEX equity−MMR / 无 margin 面=balance。
package dashboard

import (
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

func TestNavStaleNote_MarksFrozenPool(t *testing.T) {
	now := time.Now()
	pools := []pgstore.ExplorerIdlePool{
		// 62311223 实录形：mt5/USD eq=172.95 @287h 冻值——须被标出。
		{Venue: "62311223", VenueType: "mt5", Currency: "USD", Equity: 172.95, FetchedAt: now.Add(-287 * time.Hour)},
		{Venue: "okx", VenueType: "spot", Currency: "USDT", Equity: 500, FetchedAt: now.Add(-time.Minute)},
		{Venue: "ghost", VenueType: "mt5", Currency: "USD", Equity: 9, FetchedAt: now.Add(-100 * time.Hour), Ghost: true},
	}
	rep := pgstore.SumNav(pools, now)
	n, usd, detail := navStaleNote(rep.Pools, now)
	if n != 1 || usd != 172.95 {
		t.Fatalf("陈池计数/金额错: n=%d usd=%v", n, usd)
	}
	if detail == "" {
		t.Fatal("陈池清单空——下钻可见性缺")
	}
	// 幽灵池+鲜行不打标（n=1 已隐证；显式断言 Stale 旗落点）。
	if !rep.Pools[0].Stale || rep.Pools[1].Stale {
		t.Fatalf("Stale 旗错: %+v", rep.Pools)
	}
}

func TestPoolDeployableUSD(t *testing.T) {
	// MT：free_margin 权威（balance 含保证金占用——实仓开启日不得高估）。
	if got := poolDeployableUSD(pgstore.ExplorerIdlePool{VenueType: "mt5", Balance: 1000, Equity: 1010, FreeMargin: 640}); got != 640 {
		t.Fatalf("MT 应取 free_margin=640, got %v", got)
	}
	// CEX margin 面：equity−MMR（buffer=equity/MMR → buffer=2 即 MMR 半占）。
	if got := poolDeployableUSD(pgstore.ExplorerIdlePool{VenueType: "perp", Venue: "okx", Equity: 1000, Balance: 980, BufferMultiple: 2}); got != 500 {
		t.Fatalf("CEX 应 equity−MMR=500, got %v", got)
	}
	// 无 margin 面（spot/buffer 未采）=balance 兜底原口径。
	if got := poolDeployableUSD(pgstore.ExplorerIdlePool{VenueType: "spot", Venue: "okx", Balance: 300, Equity: 300}); got != 300 {
		t.Fatalf("spot 应 balance=300, got %v", got)
	}
	// CEX 穿仓边界：MMR>equity → 0 不负值。
	if got := poolDeployableUSD(pgstore.ExplorerIdlePool{VenueType: "perp", Equity: 100, BufferMultiple: 0.5}); got != 0 {
		t.Fatalf("穿仓应 0, got %v", got)
	}
}
