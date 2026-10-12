package dashboard

// D-543 验收锚：0157 种子五行 enabled 在场（迁移二跑幂等）+ 有行=「未设敞口闸」warn 清零。

import (
	"context"
	"testing"

	"arb/internal/platform/testutil/storetest"
	"arb/internal/store/pgstore"
)

var d543SeedKeys = map[string]float64{
	"binance:spot":    20000,
	"binance:perp":    1300,
	"binance:funding": 1000,
	"gate:perp":       100,
	"okx:perp":        1500, // D-547 0159 重键名（okx:unified 幽灵键→配置域 okx:perp）
}

// A1/A2① 种子行经迁移落库：五键在场、enabled、cap 值=稿定取整；二跑迁移幂等零变化。
func TestD543_SeedRowsMigratedIdempotent(t *testing.T) {
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil { // 二跑：isApplied 跳过+ON CONFLICT 双保险
		t.Fatalf("re-migrate: %v", err)
	}
	caps, err := st.ListVenueExposureCaps(ctx)
	if err != nil {
		t.Fatalf("list caps: %v", err)
	}
	for key, want := range d543SeedKeys {
		c, ok := caps[key]
		if !ok {
			t.Fatalf("种子行 %s 缺席", key)
		}
		if !c.Enabled || c.CapUSD != want {
			t.Fatalf("种子行 %s: %+v want cap=%v enabled", key, c, want)
		}
	}
}

// A2② warn 逻辑：闸表有行（种子五键）→ 「未设敞口闸」warn 不再透出（跨组合双腿全 CEX 键）。
func TestD543_SeededCapsNoWarn(t *testing.T) {
	f, s := gradFixture("long_cfd_short_perp")
	f.caps = map[string]pgstore.VenueExposureCap{}
	for key, cap := range d543SeedKeys {
		f.caps[key] = pgstore.VenueExposureCap{VenueKey: key, CapUSD: cap, Enabled: true}
	}
	warns, rej := s.exposureCapGate(context.Background(), []exposureLeg{
		{VenueKey: "binance:spot", Canonical: "BTCUSDT"},
		{VenueKey: "okx:perp", Canonical: "BTCUSDT", SellLeg: true},
	}, 100, nil)
	if rej != "" {
		t.Fatalf("种子行在场不应拒: %s", rej)
	}
	for _, w := range warns {
		t.Fatalf("种子行在场不应有 warn: %s", w)
	}
}
