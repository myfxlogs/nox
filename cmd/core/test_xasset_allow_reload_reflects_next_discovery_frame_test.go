// d017_allow_reflection_test.go —— D-017 R1 验收锚：ReloadXAssetAllowCombos 后下一帧
// 发现立即反映——detector Allow 延迟绑定 Engine.XAssetAllowMatch（热重载 holder 直读，
// 无第二加载器无 TTL 缓存）；绑定前 boot 窗口=零候选 fail-closed；吊销 reload 后下一帧归零。
package main

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/platform/testutil"
	"nox/internal/research/detector"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
	"nox/internal/trading/autotrade"
)

// d017FP detector.FactProvider 最小实现（funding 37% 过门槛；swap/modes/is_real 空=fail-open）。
type d017FP struct{}

func (d017FP) FundingFacts() map[string]map[string]float64 {
	return map[string]map[string]float64{"BTCUSDT": {"gate": 37.0}}
}
func (d017FP) SwapFacts() map[string]map[string][2]float64 { return nil }
func (d017FP) TradeModes() map[string]map[string]int64     { return nil }
func (d017FP) IsRealMap() map[string]*bool                 { return nil }

func d017Snapshot(now time.Time) map[string]map[string]bus.Quote {
	return map[string]map[string]bus.Quote{
		"BTCUSDT": {
			"gate:spot": {Symbol: "BTCUSDT", Venue: "gate", VenueType: "spot", Bid: 100000, Ask: 100010, Time: now},
			"gate:perp": {Symbol: "BTCUSDT", Venue: "gate", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now},
		},
	}
}

func TestXAssetAllowReload_ReflectsNextDiscoveryFrame(t *testing.T) {
	ctx := context.Background()
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `TRUNCATE xasset_allow_combos`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() { _, _ = st.Pool().Exec(ctx, `TRUNCATE xasset_allow_combos`) })

	eng := autotrade.New(gate.Config{Environment: "real"}, st, nil, nil, nil)
	var ref xassetAllowRef
	ref.Bind(eng) // 生产同形：atEngine 装配后接通（runServices xassetAllowBind）
	now := time.Now()
	frame := func() []detector.Candidate {
		d := &detector.FundingHedgeDetector{MinNetAnnualized: 4.0, FreshnessTTL: 30 * time.Second, Allow: ref.match}
		return d.Scan(ctx, d017Snapshot(now), d017FP{})
	}

	// 空 allowlist=零候选（fail-closed）
	if got := frame(); len(got) != 0 {
		t.Fatalf("空 allowlist 应零候选，实际 %d", len(got))
	}
	// 插 enabled 行（venue_key_* 为生成列，插 venue+venue_type 基列）→ reload → 下一帧发现立即反映（1 候选）
	if _, err := st.Pool().Exec(ctx, `INSERT INTO xasset_allow_combos
		(canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell, max_long_qty, max_short_qty, max_gross_usd, enabled, simulated, note)
		VALUES ('BTCUSDT','BTCUSDT','gate','spot','gate','perp',1000,1000,50000,true,false,'D-017 R1 验收锚')`); err != nil {
		t.Fatalf("insert allow row: %v", err)
	}
	if err := eng.ReloadXAssetAllowCombos(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := frame()
	if len(got) != 1 || got[0].VenueBuy != "gate:spot" || got[0].VenueSell != "gate:perp" {
		t.Fatalf("reload 后下一帧发现应立即反映 1 候选，实际 %+v", got)
	}
	// 吊销 → reload → 下一帧归零
	if _, err := st.Pool().Exec(ctx, `UPDATE xasset_allow_combos SET enabled=false`); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := eng.ReloadXAssetAllowCombos(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := frame(); len(got) != 0 {
		t.Fatalf("吊销 reload 后下一帧应零候选，实际 %d", len(got))
	}
	// 绑定前 boot 窗口语义：未接通 ref=零候选 fail-closed
	var unbound xassetAllowRef
	d := &detector.FundingHedgeDetector{MinNetAnnualized: 4.0, FreshnessTTL: 30 * time.Second, Allow: unbound.match}
	if got := len(d.Scan(ctx, d017Snapshot(now), d017FP{})); got != 0 {
		t.Fatalf("未 Bind 应零候选（boot 窗口 fail-closed），实际 %d", got)
	}
}
