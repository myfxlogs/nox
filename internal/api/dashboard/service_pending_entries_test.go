// service_pending_entries_test.go —— D-478 S5：待入场七闸序+安全档位分支矩阵。
package dashboard

import (
	"context"
	"testing"
	"time"

	"strconv"
	"strings"

	"nox/internal/platform/combo"
	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"
)

func d478Svc(t *testing.T) (*Service, *pgstore.Store) {
	t.Helper()
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE xasset_allow_combos, autotrade_allow_rules,
		fin_venue_symbol_flags, fin_venue_account_snapshot, positions, position_legs, alerts, ticks RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	svc := &Service{store: st, tierState: map[string]tierState{}}
	// 测试残留清理：本文件种下的 901-903 组合行会污染同包后续断言（D-471 Upsert 实证）。
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM xasset_allow_combos WHERE id BETWEEN 901 AND 903;
			DELETE FROM autotrade_allow_rules WHERE value IN ('3001157:mt5','gate:perp');
			DELETE FROM positions WHERE opportunity_id LIKE 'XASSET_CARRY:XPTUSD:%'`)
	})
	return svc, st
}

func d478Combo(t *testing.T, st *pgstore.Store, id int64, enabled bool, notional *float64, maxGross float64) {
	d478ComboLegs(t, st, id, enabled, notional, maxGross, "XPTUSD", "XPTUSDT")
}

func d478ComboLegs(t *testing.T, st *pgstore.Store, id int64, enabled bool, notional *float64, maxGross float64, canonBuy, canonSell string) {
	t.Helper()
	notionalExpr := "NULL"
	if notional != nil {
		notionalExpr = ftoaD(*notional)
	}
	if _, err := st.Pool().Exec(context.Background(), `INSERT INTO xasset_allow_combos
		(id, canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell, max_long_qty, max_short_qty, max_gross_usd, notional_usd, enabled, simulated, source)
		VALUES ($1,'`+canonBuy+`','`+canonSell+`','3001157','mt5','gate','perp',1,1,$2,`+notionalExpr+`,$3,false,'manual_seed')
		ON CONFLICT (id) DO UPDATE SET enabled=EXCLUDED.enabled, notional_usd=EXCLUDED.notional_usd, max_gross_usd=EXCLUDED.max_gross_usd`,
		id, maxGross, enabled); err != nil {
		t.Fatal(err)
	}
}

func ftoaD(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func stringsHas(s, sub string) bool { return strings.Contains(s, sub) }

// TestD478_MTSellLegKind D-478 返修 R6：MT 卖腿组合 kind=XASSET_CARRY——
// 生产实证 row1 CLUSDT(gate:perp)×WTI(277842155:mt5) 误报「构型未覆盖」（原推断缺 MT 卖腿分支）。
func TestD478_MTSellLegKind(t *testing.T) {
	svc, st := d478Svc(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO xasset_allow_combos
		(id, canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell, max_long_qty, max_short_qty, max_gross_usd, enabled, simulated, source)
		VALUES (904,'CL','WTI','gate','perp','277842155','mt5',1,1,1000,true,false,'manual_seed')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.Pool().Exec(ctx, `DELETE FROM xasset_allow_combos WHERE id=904`) })
	rows, err := svc.pendingEntries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ComboId != 904 {
			continue
		}
		if r.EntryKind != "XASSET_CARRY" {
			t.Fatalf("MT 卖腿组合 kind=%s，want XASSET_CARRY（R6）", r.EntryKind)
		}
		return
	}
	t.Fatal("id=904 行缺失")
}

func TestD478_PendingGateOrder(t *testing.T) {
	svc, st := d478Svc(t)
	ctx := context.Background()
	d478Combo(t, st, 901, true, nil, 1000)

	// 闸 1：无 allow_rules → venue 未放行（首批预期态）。
	rows, err := svc.pendingEntries(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if rows[0].WaitReason != "通道未放行: 3001157:mt5" {
		t.Fatalf("闸 1 reason=%q", rows[0].WaitReason)
	}
	// 闸 1 通过（补 venue 行）→ 闸 3 regime 未持续（无 facts）。
	if _, err := st.Pool().Exec(ctx, `INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated) VALUES ('venue','3001157:mt5',true,false),('venue','gate:perp',true,false)`); err != nil {
		t.Fatal(err)
	}
	rows, err = svc.pendingEntries(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if rows[0].EntryKind != "XASSET_CARRY" || !stringsHas(rows[0].WaitReason, "息差环境未持续") {
		t.Fatalf("闸 3 kind=%s reason=%q", rows[0].EntryKind, rows[0].WaitReason)
	}

	// 在持仓 → 不列入（gate 防重同源 opp-id 匹配）。
	key := combo.Key("XASSET_CARRY", "XPTUSD", "3001157:mt5", "gate:perp")
	var posID int64
	if err := st.Pool().QueryRow(ctx, `INSERT INTO positions (opportunity_id,type,status,expected_net_bps,opened_at)
		VALUES ($1,'XASSET_CARRY','OPEN',10,now()) RETURNING id`, key).Scan(&posID); err != nil {
		t.Fatal(err)
	}
	defer st.Pool().Exec(ctx, `DELETE FROM positions WHERE id=$1`, posID)
	rows, err = svc.pendingEntries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("在持仓组合应跳过（归持仓区），got %d 行", len(rows))
	}
}

func TestD478_TierBranches(t *testing.T) {
	svc, st := d478Svc(t)
	ctx := context.Background()
	n1200 := 1200.0
	d478Combo(t, st, 902, true, &n1200, 5000)

	// MaxGrossUSD 回退分支（notional NULL；约束禁 max_gross=0 → unset 分支由实现兜底）。
	d478ComboLegs(t, st, 903, true, nil, 2000, "XPDUSD", "XPDUSDT")
	svc.SetCrossCap(0.5)

	// 缺快照 → missing_data。
	rows, err := svc.pendingEntries(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	for _, r := range rows {
		if r.Tier != "missing_data" && r.Tier != "unset" {
			t.Fatalf("无快照/未设档位应 missing_data|unset，got %s", r.Tier)
		}
	}

	// 快照新鲜+equity=100 → needed 1200 > 100×0.5 → over+warn（crossCap 注入生效）。
	v100, v5000 := 100.0, 5000.0 // #1c：腿池资金=实时态种子（快照表=历史记录不再喂门禁）
	seedLiveAccounts(t, svc, []pgstore.VenueAccountLatest{
		{Venue: "gate", VenueType: "perp", BalanceRaw: &v100, EquityRaw: &v100, FreeMarginRaw: &v100, Currency: "USDT", FetchedAt: time.Now()},
		{Venue: "3001157", VenueType: "mt5", BalanceRaw: &v5000, EquityRaw: &v5000, FreeMarginRaw: &v5000, Currency: "USDT", FetchedAt: time.Now()},
	})
	rows, err = svc.pendingEntries(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	for _, r := range rows {
		if r.ComboId == 902 && (r.Tier != "over" || !r.TierWarn || !stringsHas(r.TierDetail, "超出安全线：计划仓位 $1200 > 账户 gate 净值 $100 的 0.5 倍上限")) {
			t.Fatalf("超档分支: tier=%s detail=%q warn=%v", r.Tier, r.TierDetail, r.TierWarn)
		}
		if r.ComboId == 903 && (r.Tier != "over" || !stringsHas(r.TierDetail, "计划仓位 $1000")) {
			t.Fatalf("notional NULL→MaxGross/2 单腿口径回退分支（1000>50 应 over），tier=%s detail=%q", r.Tier, r.TierDetail)
		}
	}
}
