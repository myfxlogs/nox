// d512_t9_cooldown_test.go —— D-512 T9：combo 冷却三态红测。
// incident_attributed 结算 → 拒入（cooling）→ 过 30d → 复资格（expired）；
// 非事故亏损结算/无结算 combo = clean。
package pgstore

import (
	"context"
	"testing"
	"time"
)

func TestD512_T9_ComboCooldown(t *testing.T) {
	st := sharedTestStore(t) // 全量 Migrate 同态 schema（裸 New 不含 0148/0149 新迁移）
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE positions, position_legs, position_settlements, xasset_allow_combos RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Pool().Exec(ctx, `TRUNCATE positions, position_legs, position_settlements, xasset_allow_combos RESTART IDENTITY CASCADE`)
	})

	// 种 combo 两行：A=事故组合、B=clean 组合。
	for _, c := range []struct {
		id     int64
		cb, vs string
	}{
		{901, "XAUUSD", "XPTUSDT"}, {902, "XPDUSD", "XPDUSDT"},
	} {
		vs := "gate"
		if c.id == 902 {
			vs = "okx"
		}
		if _, err := st.Pool().Exec(ctx, `INSERT INTO xasset_allow_combos
			(id, canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell, max_long_qty, max_short_qty, max_gross_usd, enabled, simulated, source)
			VALUES ($1,$2,$3,'3001157','mt5',$4,'perp',1,1,1000,true,false,'manual_seed')`,
			c.id, c.cb, c.vs, vs); err != nil {
			t.Fatal(err)
		}
	}

	seedPosition := func(opp string) int64 {
		var id int64
		if err := st.Pool().QueryRow(ctx, `INSERT INTO positions
			(opportunity_id, type, status, expected_net_bps, environment, opened_at, closed_at)
			VALUES ($1,'XASSET_CARRY','CLOSED',10,'live', now()-interval '3d', now()-interval '2d') RETURNING id`, opp).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	seedLeg := func(posID int64, legNo int, side, venue, symbol string) {
		if _, err := st.Pool().Exec(ctx, `INSERT INTO position_legs
			(position_id, leg_no, phase, venue, symbol, side, qty, price_expected, status)
			VALUES ($1,$2,'OPEN',$3,$4,$5,1,100,'FILLED')`, posID, legNo, venue, symbol, side); err != nil {
			t.Fatal(err)
		}
	}

	// A：事故组合——BUY 腿=XAUUSD@3001157、SELL 腿=XPTUSDT@gate，incident_attributed 结算。
	posA := seedPosition("d512-t9-incident")
	seedLeg(posA, 1, "BUY", "3001157", "XAUUSD")
	seedLeg(posA, 2, "SELL", "gate", "XPTUSDT")
	if _, err := st.Pool().Exec(ctx, `INSERT INTO position_settlements
		(position_id, realized_usd, skipped_reason, incident_attributed, settled_at)
		VALUES ($1, -20, 'legged', true, now()-interval '1d')`, posA); err != nil {
		t.Fatal(err)
	}

	// B：clean 组合——盈利结算（net>0 且非事故）→ 不冷却（SSOT OR 语义：净亏即冷却，
	// 非事故+盈利=clean 对照）。
	posB := seedPosition("d512-t9-clean")
	seedLeg(posB, 1, "BUY", "3001157", "XPDUSD")
	seedLeg(posB, 2, "SELL", "okx", "XPDUSDT")
	if _, err := st.Pool().Exec(ctx, `INSERT INTO position_settlements
		(position_id, realized_usd, incident_attributed, settled_at)
		VALUES ($1, 3.5, false, now()-interval '1d')`, posB); err != nil {
		t.Fatal(err)
	}

	// 三态断言：A cooling 拒入；B clean 保留。
	rows, err := st.LoadXAssetAllowCombos(ctx, "live")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.CanonicalBuy+"|"+r.VenueSell] = true
	}
	if got["XAUUSD|gate:perp"] {
		t.Error("事故组合 A 应 cooling 拒入")
	}
	if !got["XPDUSD|okx:perp"] {
		t.Error("clean 组合 B（盈利非事故）应保留")
	}

	// expired：mock 时钟过 30d → 复资格（直查 ApplyComboCooldown）。
	kept, cutoffs, err := st.ApplyComboCooldown(ctx, []XAssetAllowRow{
		{CanonicalBuy: "XAUUSD", CanonicalSell: "XPTUSDT", VenueBuy: "3001157:mt5", VenueSell: "gate:perp"},
	}, time.Now().Add(31*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Fatalf("过 30d 应 expired 复资格，kept=%d cutoffs=%v", len(kept), cutoffs)
	}
	// cooling 截止透出断言。
	_, cut2, err := st.ApplyComboCooldown(ctx, []XAssetAllowRow{
		{CanonicalBuy: "XAUUSD", CanonicalSell: "XPTUSDT", VenueBuy: "3001157:mt5", VenueSell: "gate:perp"},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(cut2) != 1 {
		t.Fatalf("cooling 行应透出截止：%v", cut2)
	}
}
