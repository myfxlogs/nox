package dashboard

// D-547 验收锚：T1 0159 迁移后 real 组合全行 2×notional≤gross；T2 闸键域零幽灵键；
// T3 闸表面透出 live 面值+超闸；T4 okx/gate 三档分类（fixture 在 reconcile 侧）；T5 组合表 live 字段。

import (
	"context"
	"testing"
	"time"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"

	"connectrpc.com/connect"
)

// —— T1：0159 迁移修正——real 行 2×notional≤gross + okx:unified→okx:perp 重键名（幂等）。

func TestD547_T1_ComboGrossAlignedByMigration(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	// 干跑锚：自锁行（2×notional>gross）+幽灵键按生产形态先播（0159 未跑时它们在场）。
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell,
			max_long_qty, max_short_qty, max_gross_usd, notional_usd, enabled, simulated)
		VALUES ('BTCUSDT','BTCUSD','okx','perp','3001157','mt5', 1000, 1, 1000, 1200, true, false)
		ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed combo: %v", err)
	}
	// 自锁行播在首迁之后——抹 0159 版本记录重跑=干跑同式（预期恰命中本行）。
	if _, err := st.Pool().Exec(ctx, `DELETE FROM schema_migrations WHERE version=159 OR version>=1000`); err != nil {
		t.Fatalf("reset 0159: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 全 real 行 2×notional≤gross。
	rows, err := st.ListXAssetAllowCombosAll(ctx)
	if err != nil {
		t.Fatalf("list combos: %v", err)
	}
	for _, r := range rows {
		if r.NotionalUSD != nil && 2**r.NotionalUSD > r.MaxGrossUSD {
			t.Fatalf("combo #%d 仍自锁: 2×%.0f>%.0f", r.ID, *r.NotionalUSD, r.MaxGrossUSD)
		}
	}
	// 幽灵键重键名：unified 不在、perp 在。
	caps, err := st.ListVenueExposureCaps(ctx)
	if err != nil {
		t.Fatalf("list caps: %v", err)
	}
	if _, ok := caps["okx:unified"]; ok {
		t.Fatal("okx:unified 幽灵键仍在")
	}
	if c, ok := caps["okx:perp"]; !ok || c.CapUSD != 1500 {
		t.Fatalf("okx:perp 未重键名到位: %+v", c)
	}
	// 二跑幂等零漂移。
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	caps2, _ := st.ListVenueExposureCaps(ctx)
	if _, ok := caps2["okx:unified"]; ok {
		t.Fatal("二跑后 unified 复现")
	}
}

// —— T2：闸键域审计——caps 键 ⊆ 账户面键（exchange_configs ∪ broker_accounts），幽灵键显报。

func TestD547_T2_NoGhostCapKeys(t *testing.T) {
	accountKeys := []string{"okx:perp", "okx:spot", "gate:perp", "binance:perp", "3001157:mt5"}
	caps := map[string]pgstore.VenueExposureCap{
		"okx:perp":    {VenueKey: "okx:perp", CapUSD: 1500},
		"gate:perp":   {VenueKey: "gate:perp", CapUSD: 100},
		"okx:unified": {VenueKey: "okx:unified", CapUSD: 1500}, // 幽灵键=交换配置键域不存在
	}
	ghosts := ghostCapKeys(caps, accountKeys)
	if len(ghosts) != 1 || ghosts[0] != "okx:unified" {
		t.Fatalf("ghost keys=%v（应恰 okx:unified 一行）", ghosts)
	}
	delete(caps, "okx:unified")
	if g := ghostCapKeys(caps, accountKeys); len(g) != 0 {
		t.Fatalf("迁移后幽灵键残留: %v", g)
	}
}

// —— T3：闸表面 live 面值（allocated/live_available/live_source/over_cap）。

func TestD547_T3_CapRowsLiveValues(t *testing.T) {
	f, s := gradFixture("long_cfd_short_perp")
	f.caps = map[string]pgstore.VenueExposureCap{
		"gate:perp": {VenueKey: "gate:perp", CapUSD: 100, Enabled: true, UpdatedAt: time.Now()},
	}
	f.allowRows = []pgstore.XAssetAllowRow{{
		CanonicalBuy: "XAUUSDT", CanonicalSell: "XAUUSD", VenueBuy: "gate:perp", VenueSell: "3001157:mt5",
		MaxGrossUSD: 2000, NotionalUSD: f64p(1750),
	}}
	seedLiveAccounts(t, s, []pgstore.VenueAccountLatest{{
		Venue: "gate", VenueType: "perp", EquityRaw: f64p(80), Currency: "USDT", FetchedAt: time.Now(),
	}})
	resp, err := s.ListVenueExposureCaps(context.Background(), connect.NewRequest(&v1.ListVenueExposureCapsRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var row *v1.VenueExposureCapRow
	for _, r := range resp.Msg.Rows {
		if r.VenueKey == "gate:perp" {
			row = r
		}
	}
	if row == nil {
		t.Fatal("gate:perp 行缺")
	}
	if row.AllocatedUsd != 1750 {
		t.Fatalf("allocated=%v（口径=notional 优先应 1750）", row.AllocatedUsd)
	}
	if !row.OverCap {
		t.Fatalf("over_cap 应真（allocated 1750>cap 100）: %+v", row)
	}
	if row.LiveAvailableUsd != 80 || row.LiveSource != "snap" {
		t.Fatalf("live 面值: avail=%v src=%q", row.LiveAvailableUsd, row.LiveSource)
	}
}

// —— T5：组合表 active_notional/双腿池/闸冲突。

func TestD547_T5_ComboRowsLiveConflict(t *testing.T) {
	f, s := gradFixture("long_cfd_short_perp")
	n := 1750.0
	f.adminRows = []pgstore.XAssetComboAdminRow{{
		ID: 12, CanonicalBuy: "XAUUSDT", CanonicalSell: "XAUUSD",
		VenueBuy: "gate:perp", VenueSell: "3001157:mt5",
		MaxLongQty: 500, MaxShortQty: 1, MaxGrossUSD: 5000, NotionalUSD: &n, Enabled: true,
	}}
	f.comboOpen = map[string][]int64{"XAUUSDT|gate:perp": {501}}
	f.comboOpenN = map[string]float64{"XAUUSDT|gate:perp": 1800}
	seedLiveAccounts(t, s, []pgstore.VenueAccountLatest{
		{Venue: "gate", VenueType: "perp", EquityRaw: f64p(80), Currency: "USDT", FetchedAt: time.Now()},
		{Venue: "3001157", VenueType: "mt5", EquityRaw: f64p(9000), Currency: "USD", FetchedAt: time.Now()},
	}) // D-564 U2 退役：gate 保证金率判据不再吃配置杠杆——pool_verdict=unknown（如实标缺）
	resp, err := s.ListXAssetAllowCombos(context.Background(), connect.NewRequest(&v1.ListXAssetAllowCombosRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.Msg.Rows) != 1 {
		t.Fatalf("rows=%d", len(resp.Msg.Rows))
	}
	r := resp.Msg.Rows[0]
	if r.ActiveNotionalUsd != 1800 {
		t.Fatalf("active_notional=%v", r.ActiveNotionalUsd)
	}
	if r.CapConflict {
		t.Fatalf("cap_conflict 应假（D-564 U2：gate 保证金率判据退役，配置杠杆不再推出 conflict）: %+v", r)
	}
	if r.PoolVerdict != "unknown" {
		t.Fatalf("gate 腿 pool_verdict 应 unknown（判据退役如实标缺）, got %s", r.PoolVerdict)
	}
	if r.BuyPoolUsd != 80 || r.PoolSource != "snap" {
		t.Fatalf("buy_pool=%v src=%q", r.BuyPoolUsd, r.PoolSource)
	}
	if r.SellPoolUsd != 9000 {
		t.Fatalf("sell_pool=%v", r.SellPoolUsd)
	}
}
