// funding_canonical_norm_test.go —— 读径归一锚：`_USDT` 残形 canonical 命中
// 归一形 facts 行（#473 开仓卡死病灶=读侧未 BookCanonical）。
package autotrade

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/fact"
)

func TestFundingReadPath_CanonicalNorm(t *testing.T) {
	s := testAccumStore(t)
	e := &Engine{store: s}
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindFunding, Venue: "gate", Symbol: "SPX500USDT", Value: 12.3,
			Unit: fact.UnitPctAnnualized, Ts: time.Now(), Src: "t"},
	}); err != nil {
		t.Fatalf("seed fact: %v", err)
	}
	// 残形 SPX500_USDT（combos #31 canonical_buy 实测形）→读侧归一命中。
	if _, _, ok := e.fundingRateForVenue(ctx, "gate", "SPX500_USDT"); !ok {
		t.Fatalf("残形 canonical 应经 BookCanonical 归一命中归一形行")
	}
	if _, _, ok := e.fundingRateMeanForVenue(ctx, "gate", "SPX500_USDT", 24*time.Hour); !ok {
		t.Fatalf("窗均径残形应归一命中")
	}
}
