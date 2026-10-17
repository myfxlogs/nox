package pgstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func seedPitDir(t *testing.T) string {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "xau-pit.md"),
		[]byte("# x\n**判据挂接**：EA-09\nmatch:\n  canonicals: [XAUUSD]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPitGate_InsertRejectExemptDisable(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	dir := seedPitDir(t)
	t.Setenv("NOX_ECONKB_DIR", dir)

	// ①canonical 命中坑→enable 物理拒
	_, err := st.InsertXAssetAllowCombo(ctx, &XAssetComboInsert{
		CanonicalBuy: "XAUUSD", CanonicalSell: "PAXGUSDT",
		VenueBuy: "gate", VenueSell: "binance", Source: "manual_seed",
		MaxGrossUSD: 100, MaxLongQty: 1, MaxShortQty: 1,
	})
	if err == nil {
		t.Fatal("坑签名命中组合 enable 应物理拒")
	}
	t.Logf("拒=PASS: %v", err)

	// ②D# 豁免引用落行→过
	id, err := st.InsertXAssetAllowCombo(ctx, &XAssetComboInsert{
		CanonicalBuy: "XAUUSD", CanonicalSell: "PAXGUSDT",
		VenueBuy: "gate", VenueSell: "binance", Source: "manual_seed",
		MaxGrossUSD: 100, MaxLongQty: 1, MaxShortQty: 1,
		PitExemptD: "D-587",
	})
	if err != nil {
		t.Fatalf("豁免过: %v", err)
	}
	if id == 0 {
		t.Fatal("豁免行 id=0")
	}

	// ③未命中坑→过
	id2, err := st.InsertXAssetAllowCombo(ctx, &XAssetComboInsert{
		CanonicalBuy: "TRXUSD", CanonicalSell: "BTCUSDT",
		VenueBuy: "gate", VenueSell: "binance", Source: "manual_seed",
		MaxGrossUSD: 100, MaxLongQty: 1, MaxShortQty: 1,
	})
	if err != nil {
		t.Fatalf("未命中过: %v", err)
	}

	// ④评估环：豁免行不摘/未命中行不摘；手工再插一条无豁免命中行→摘
	// （同 canonical_buy 换 venue 避唯一键）
	_, err = st.InsertXAssetAllowCombo(ctx, &XAssetComboInsert{
		CanonicalBuy: "XAUUSD", CanonicalSell: "PAXGUSDT",
		VenueBuy: "okx", VenueSell: "binance", Source: "manual_seed",
		MaxGrossUSD: 100, MaxLongQty: 1, MaxShortQty: 1,
	})
	if err == nil {
		t.Fatalf("okx 变体应同拒")
	}
	// 直接落 enabled 行绕闸（模拟闸前存量 armed 行）
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_sell,
			enabled, simulated, note, max_gross_usd, source, sizing_mode)
		VALUES ('XAUUSD','CLUSDT','gate','okx',true,false,'armed 存量',100,'graduation','fixed')`); err != nil {
		t.Fatal(err)
	}
	n, err := st.DisablePitHitCombos(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("评估环摘=%d want ≥1", n)
	}
	// 定向：本种 armed 存量行被摘（note 带坑闸痕）
	var en bool
	var note string
	if err := st.Pool().QueryRow(ctx, `
		SELECT enabled, note FROM xasset_allow_combos
		WHERE canonical_buy='XAUUSD' AND canonical_sell='CLUSDT' AND venue_buy='gate'`).Scan(&en, &note); err != nil {
		t.Fatal(err)
	}
	if en || note == "armed 存量" {
		t.Fatalf("存量命中行未被摘留痕: enabled=%v note=%s", en, note)
	}
	// 豁免/未命中行仍 enabled
	for _, id := range []int64{id, id2} {
		var en bool
		if err := st.Pool().QueryRow(ctx,
			`SELECT enabled FROM xasset_allow_combos WHERE id=$1`, id).Scan(&en); err != nil {
			t.Fatal(err)
		}
		if !en {
			t.Fatalf("combo#%d 被误摘", id)
		}
	}
}

// TestDisabledCombosOpenPositions D-599 锚：摘牌组合的 OPEN 持仓在平仓触发集。
func TestDisabledCombosOpenPositions(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	// enabled=false 组合+OPEN 持仓→在集；enabled=true 组合持仓→不在集
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_sell,
			enabled, simulated, note, max_gross_usd, source, sizing_mode) VALUES
		('CLOSEA','CLOSEB','gate','binance',false,false,'摘牌',100,'manual_seed','fixed'),
		('CLOSEC','CLOSED','gate','binance',true,false,'活',100,'manual_seed','fixed')`); err != nil {
		t.Fatal(err)
	}
	// 持仓行（positions+position_legs）
	var pid1, pid2 int64
	for _, canon := range []string{"CLOSEA", "CLOSEC"} {
		var pid int64
		if err := st.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1,'XASSET_CARRY','OPEN',now()) RETURNING id`,
			"test-"+canon).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if canon == "CLOSEA" {
			pid1 = pid
		} else {
			pid2 = pid
		}
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, status)
			VALUES ($1,1,'OPEN','gate','perp',$2,'BUY',1,100,'FILLED')`, pid, canon); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := st.DisabledCombosOpenPositions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var has1, has2 bool
	for _, id := range ids {
		if id == pid1 {
			has1 = true
		}
		if id == pid2 {
			has2 = true
		}
	}
	if !has1 || has2 {
		t.Fatalf("摘牌组合持仓在集/活组合持仓不在集：got=%v pid1=%v pid2=%v", ids, has1, has2)
	}
}
