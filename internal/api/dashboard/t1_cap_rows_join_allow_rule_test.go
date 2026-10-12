package dashboard

// D-557 验收锚：T1 ListVenueExposureCaps merged 行 join 通道白名单
// （有规则行→rule_id+enabled 正确透出；无行→id=0；unset 合成行同 join）。

import (
	"context"
	"testing"
	"time"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"

	"connectrpc.com/connect"
)

func TestD557_T1_CapRowsJoinAllowRule(t *testing.T) {
	f, s := gradFixture("long_cfd_short_perp")
	f.caps = map[string]pgstore.VenueExposureCap{
		"gate:perp": {VenueKey: "gate:perp", CapUSD: 100, Enabled: true, UpdatedAt: time.Now()},
	}
	// venue 规则：gate:perp real enabled / 1279967:mt5 仅 sim 行（join 仍须命中——
	// venue_key 键域无 sim 维度，real 优先但无 real 行回落 sim）。
	f.allowRules = []pgstore.AutotradeAllowRule{
		{ID: 7, Kind: "venue", Value: "gate:perp", Enabled: true},
		{ID: 8, Kind: "venue", Value: "gate:perp", Enabled: false, Simulated: true}, // sim 行不得压 real
		{ID: 9, Kind: "venue", Value: "1279967:mt5", Enabled: true, Simulated: true},
		{ID: 10, Kind: "canonical", Value: "BTC", Enabled: true}, // 非 venue 行不 join
	}
	f.accts = []pgstore.BrokerAccount{{Venue: "1279967", Platform: 1}}
	resp, err := s.ListVenueExposureCaps(context.Background(), connect.NewRequest(&v1.ListVenueExposureCapsRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byKey := map[string]*v1.VenueExposureCapRow{}
	for _, r := range resp.Msg.Rows {
		byKey[r.VenueKey] = r
	}
	g := byKey["gate:perp"]
	if g == nil || g.AllowRuleId != 7 || !g.AllowEnabled {
		t.Fatalf("gate:perp join 应取 real 行 id=7 enabled=true, got %+v", g)
	}
	// unset 合成行（1279967:mt5 有账户无闸行）——join 同生效。
	u := byKey["1279967:mt5"]
	if u == nil || !u.Unset || u.AllowRuleId != 9 || !u.AllowEnabled {
		t.Fatalf("unset 合成行 join 缺失/错误: %+v", u)
	}
	// 无规则 venue_key → id=0（fake 无第三 venue——补一个 caps 行验证）。
	f.caps["okx:perp"] = pgstore.VenueExposureCap{VenueKey: "okx:perp", CapUSD: 50, Enabled: true, UpdatedAt: time.Now()}
	resp2, err := s.ListVenueExposureCaps(context.Background(), connect.NewRequest(&v1.ListVenueExposureCapsRequest{}))
	if err != nil {
		t.Fatalf("list2: %v", err)
	}
	for _, r := range resp2.Msg.Rows {
		if r.VenueKey == "okx:perp" && (r.AllowRuleId != 0 || r.AllowEnabled) {
			t.Fatalf("okx:perp 无规则行应 id=0 enabled=false, got %+v", r)
		}
	}
}
