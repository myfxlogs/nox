// service_pending_entries_test.go —— D-478 S5：待入场七闸序+安全档位分支矩阵。
package dashboard

import (
	"context"
	"testing"

	"strconv"
	"strings"

	"arb/internal/combo"
	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

func d478Svc(t *testing.T) (*Service, *pgstore.Store) {
	t.Helper()
	st, err := pgstore.New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
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
		(id, canonical_buy, canonical_sell, venue_buy, venue_sell, max_long_qty, max_short_qty, max_gross_usd, notional_usd, enabled, simulated)
		VALUES ($1,'`+canonBuy+`','`+canonSell+`','3001157:mt5','gate:perp',1,1,$2,`+notionalExpr+`,$3,false)
		ON CONFLICT (id) DO UPDATE SET enabled=EXCLUDED.enabled, notional_usd=EXCLUDED.notional_usd, max_gross_usd=EXCLUDED.max_gross_usd`,
		id, maxGross, enabled); err != nil {
		t.Fatal(err)
	}
}

func ftoaD(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func stringsHas(s, sub string) bool { return strings.Contains(s, sub) }

func TestD478_PendingGateOrder(t *testing.T) {
	svc, st := d478Svc(t)
	ctx := context.Background()
	d478Combo(t, st, 901, true, nil, 1000)

	// 闸 1：无 allow_rules → venue 未放行（首批预期态）。
	rows, err := svc.pendingEntries(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if rows[0].WaitReason != "venue 未放行: 3001157:mt5" {
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
	if rows[0].EntryKind != "XASSET_CARRY" || !stringsHas(rows[0].WaitReason, "regime 未持续") {
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
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_venue_account_snapshot (venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at, src)
		VALUES ('gate','perp',100,100,100,'USDT',now(),'test'),('3001157','mt5',5000,5000,5000,'USDT',now(),'test')`); err != nil {
		t.Fatal(err)
	}
	rows, err = svc.pendingEntries(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	for _, r := range rows {
		if r.ComboId == 902 && (r.Tier != "over" || !r.TierWarn || !stringsHas(r.TierDetail, "超档: 名义$1200 > 权益$100×0.5")) {
			t.Fatalf("超档分支: tier=%s detail=%q warn=%v", r.Tier, r.TierDetail, r.TierWarn)
		}
		if r.ComboId == 903 && (r.Tier != "over" || !stringsHas(r.TierDetail, "名义$2000")) {
			t.Fatalf("notional NULL→MaxGross 回退分支（2000>50 应 over），tier=%s detail=%q", r.Tier, r.TierDetail)
		}
	}
}
