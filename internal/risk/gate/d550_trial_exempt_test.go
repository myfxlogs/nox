package gate

// D-550 试单豁免锚：宿主策略退役 ∧ 组合行=S2 桥试单（note 'S2桥%'）→ disabled
// 豁免放行；未标/四元组错配行维持拒。真库（NOX_TEST_PG_DSN）。

import (
	"context"
	"strings"
	"testing"
)

func TestD550_TrialComboExemptsDisabled(t *testing.T) {
	s := setupGateDisabledStore(t, "XASSET_CARRY")
	ctx := context.Background()
	// S2 桥写试单行：买 PAXGUSDT@binance:perp 卖 XAUUSD@15032313:mt5。
	if _, err := s.Pool().Exec(ctx, `DELETE FROM xasset_allow_combos
		WHERE canonical_buy='PAXGUSDT' AND canonical_sell='XAUUSD'
		  AND venue_key_buy='binance:perp' AND venue_key_sell='15032313:mt5'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO xasset_allow_combos
		(canonical_buy, canonical_sell, venue_buy, venue_sell, venue_type_buy, venue_type_sell,
		 max_long_qty, max_short_qty, max_gross_usd, enabled, simulated, note)
		VALUES ('PAXGUSDT','XAUUSD','binance','15032313','perp','mt5',5,5,200,true,false,'S2桥 假设#505 试单')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(ctx, `DELETE FROM xasset_allow_combos WHERE note LIKE 'S2桥%' AND canonical_buy='PAXGUSDT'`)
	})
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5}, nil)
	var ran bool
	r := g.Submit(ctx, &TradeIntent{
		Strategy: "XASSET_CARRY", Canonical: "PAXGUSDT", CanonicalSell: "XAUUSD",
		VenueBuy: "binance:perp", VenueSell: "15032313:mt5",
		Qty: 0.01, NetBps: 10, DedupKey: "XASSET_CARRY:PAXGUSDT:binance:perp:15032313:mt5:d550",
		Execute: func(context.Context) (string, string, int64) { ran = true; return "EXECUTED", "ok", 1 },
	})
	if r.Status == "REJECTED" && strings.Contains(r.Reason, "strategy XASSET_CARRY disabled") {
		t.Fatalf("试单行应豁免 disabled 拒: %+v", r)
	}
	if !ran && r.Status == "REJECTED" {
		t.Logf("试单意向被后续闸拒（disabled 豁免已达——后续闸照常生效）: %s", r.Reason)
	}
	// 未标行（note 无前缀）同四元组 → 仍拒。
	if _, err := s.Pool().Exec(ctx, `DELETE FROM xasset_allow_combos WHERE note LIKE 'S2桥%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO xasset_allow_combos
		(canonical_buy, canonical_sell, venue_buy, venue_sell, venue_type_buy, venue_type_sell,
		 max_long_qty, max_short_qty, max_gross_usd, enabled, simulated, note)
		VALUES ('PAXGUSDT','XAUUSD','binance','15032313','perp','mt5',5,5,200,true,false,'手工行非试单')`); err != nil {
		t.Fatal(err)
	}
	r2 := g.Submit(ctx, &TradeIntent{
		Strategy: "XASSET_CARRY", Canonical: "PAXGUSDT", CanonicalSell: "XAUUSD",
		VenueBuy: "binance:perp", VenueSell: "15032313:mt5",
		Qty: 0.01, NetBps: 10, DedupKey: "XASSET_CARRY:PAXGUSDT:binance:perp:15032313:mt5:d550b",
		Execute: func(context.Context) (string, string, int64) { t.Fatal("非试单行不应执行"); return "", "", 0 },
	})
	if r2.Status != "REJECTED" || !strings.Contains(r2.Reason, "strategy XASSET_CARRY disabled") {
		t.Fatalf("非试单行应 disabled 拒: %+v", r2)
	}
	// CanonicalSell 缺（四元组不齐）→ 试单行在册也不豁免。
	r3 := g.Submit(ctx, &TradeIntent{
		Strategy: "XASSET_CARRY", Canonical: "PAXGUSDT",
		VenueBuy: "binance:perp", VenueSell: "15032313:mt5",
		Qty: 0.01, NetBps: 10, DedupKey: "XASSET_CARRY:PAXGUSDT:binance:perp:15032313:mt5:d550c",
		Execute: func(context.Context) (string, string, int64) { t.Fatal("不应执行"); return "", "", 0 },
	})
	if r3.Status != "REJECTED" || !strings.Contains(r3.Reason, "strategy XASSET_CARRY disabled") {
		t.Fatalf("canonical_sell 缺不应豁免: %+v", r3)
	}
}
