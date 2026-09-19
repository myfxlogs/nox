// D-273 S5：判断结果 upsert 去重 + Latest 读取测试（P2-5 承重墙：ON CONFLICT 三列必须与唯一索引对齐）。
package pgstore

import (
	"context"
	"testing"
)

func TestCexEarnJudgment_UpsertDedup(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, "TRUNCATE fin_cex_earn_judgment"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}

	first := &CexEarnJudgment{Venue: "okx", Ccy: "USDT", ProductType: "flexible", APR: 0.035, Category: "stable", Verdict: "opportunity", Reasons: `["优于逆回购 4.50%","flexible 不锁定"]`}
	if err := s.UpsertCexEarnJudgment(ctx, first); err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	second := &CexEarnJudgment{Venue: "okx", Ccy: "USDT", ProductType: "flexible", APR: 0.036, Category: "stable", Verdict: "not_opportunity", Reasons: `["不如逆回购 4.50%"]`}
	if err := s.UpsertCexEarnJudgment(ctx, second); err != nil {
		t.Fatalf("upsert second（同键应覆盖不新增）: %v", err)
	}
	third := &CexEarnJudgment{Venue: "binance", Ccy: "USDT", ProductType: "flexible", APR: 0.024, Category: "stable", Verdict: "not_opportunity", Reasons: `[]`}
	if err := s.UpsertCexEarnJudgment(ctx, third); err != nil {
		t.Fatalf("upsert third（异 venue 应独立行）: %v", err)
	}

	got, err := s.LatestCexEarnJudgments(ctx)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows（同 venue+ccy+product 覆盖，异 venue 独立）, got %d", len(got))
	}
	byVenue := map[string]CexEarnJudgment{}
	for _, r := range got {
		byVenue[r.Venue] = r
	}
	okx := byVenue["okx"]
	if okx.Verdict != "not_opportunity" || okx.APR != 0.036 || okx.Reasons != `["不如逆回购 4.50%"]` {
		t.Errorf("okx 行应为第二次 upsert 覆盖后的值, got %+v", okx)
	}
	if okx.ID == 0 || okx.Ts.IsZero() {
		t.Error("RETURNING id, ts 未回填")
	}
	if bn := byVenue["binance"]; bn.Verdict != "not_opportunity" || bn.ProductType != "flexible" {
		t.Errorf("binance 行不符: %+v", bn)
	}
}
