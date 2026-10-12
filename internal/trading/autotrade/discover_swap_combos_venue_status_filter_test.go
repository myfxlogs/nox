package autotrade

// 锚 A：stopped/failed 的 broker 托管 venue 立即退出 SWAPARB 发现配对
// （不等 facts 自然过期）。三分支：stopped/failed→剔除、connected→照常、
// absent（CEX 自管 venue，不入 broker_accounts）→ 谓词层放行（isRealPair fail-closed
// 下 absent 无法出 combo，集成层锚 stopped/failed/connected，absent 由
// pgstore.BrokerVenueDiscoverable 纯函数锚覆盖——见 pgstore d388_test）。

import (
	"context"
	"testing"
	"time"

	"arb/internal/data/fact"
	"arb/internal/risk/gate"
	"arb/internal/store/pgstore"
)

func d388Seed(t *testing.T, s *pgstore.Store, longStatus, shortStatus string) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{`TRUNCATE facts`, `TRUNCATE broker_accounts CASCADE`} {
		if _, err := s.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("truncate: %v", err)
		}
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	for v, st := range map[string]string{"vA": longStatus, "vB": shortStatus} {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, is_real)
			 VALUES ($1, 'mt5', 'h', 443, 'p', 's', $2, false)`, v, st); err != nil {
			t.Fatalf("seed %s: %v", v, err)
		}
	}
}

func d388Combos(t *testing.T, s *pgstore.Store, longStatus, shortStatus string) int {
	t.Helper()
	d388Seed(t, s, longStatus, shortStatus)
	e := &Engine{store: s, cfg: gate.Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour}}
	return len(e.discoverSwapCombos(context.Background()))
}

func TestD388_DiscoverSwapCombos_VenueStatusFilter(t *testing.T) {
	s := testAccumStore(t)
	for _, tc := range []struct {
		name            string
		longSt, shortSt string
		want            int
	}{
		{"both connected", "connected", "connected", 1},
		{"long stopped", "stopped", "connected", 0},
		{"short stopped", "connected", "stopped", 0},
		{"long failed", "failed", "connected", 0},
		{"both stopped", "stopped", "stopped", 0},
		// R0-A3 第四值：status 列无 CHECK 约束，'unknown' 必须丢弃——
		// 证判据是 ≠connected 而非 ∈{stopped,failed} 枚举。
		{"long unknown", "unknown", "connected", 0},
	} {
		if got := d388Combos(t, s, tc.longSt, tc.shortSt); got != tc.want {
			t.Errorf("%s: want %d combos, got %d", tc.name, tc.want, got)
		}
	}
}
