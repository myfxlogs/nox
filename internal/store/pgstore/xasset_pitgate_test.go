package pgstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	// 持仓行：pid1=双腿命中摘牌组合（gate:CLOSEA × binance:CLOSEB）→在集；
	// pid2=活组合双腿（gate:CLOSEC × binance:CLOSED）→不在集；
	// pid3=共腿仓位（仅一腿 gate:CLOSEA 命中，另一腿 okx:OTHER 不属该组合）→共腿不误杀
	newPos := func(oppID string) int64 {
		var pid int64
		if err := st.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1,'XASSET_CARRY','OPEN',now()) RETURNING id`,
			oppID).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		return pid
	}
	newLeg := func(pid int64, no int, venue, sym string) {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, status)
			VALUES ($1,$2,'OPEN',$3,'perp',$4,'BUY',1,100,'FILLED')`, pid, no, venue, sym); err != nil {
			t.Fatal(err)
		}
	}
	pid1 := newPos("t-pair")
	newLeg(pid1, 1, "gate", "CLOSEA")
	newLeg(pid1, 2, "binance", "CLOSEB")
	pid2 := newPos("t-live")
	newLeg(pid2, 1, "gate", "CLOSEC")
	newLeg(pid2, 2, "binance", "CLOSED")
	pid3 := newPos("t-shared")
	newLeg(pid3, 1, "gate", "CLOSEA") // 共腿但另一腿不属于该组合
	newLeg(pid3, 2, "okx", "OTHERX")

	ids, err := st.DisabledCombosOpenPositions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var has1, has2, has3 bool
	for _, id := range ids {
		switch id {
		case pid1:
			has1 = true
		case pid2:
			has2 = true
		case pid3:
			has3 = true
		}
	}
	if !has1 || has2 || has3 {
		t.Fatalf("双腿配对谓词：pid1(双中)=%v pid2(活)=%v pid3(共腿)=%v ids=%v", has1, has2, has3, ids)
	}
}

// TestKillTrigger D-599 续锚：判据落表——净 PnL 超阈摘/连败摘/活组合不摘。
func TestKillTrigger(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	// 三种组合：净 PnL 闸行（阈 -50）/连败闸行（阈 2）/无判据行
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_sell,
			enabled, simulated, note, max_gross_usd, source, sizing_mode, kill_net_pnl_usd, kill_loss_streak)
		VALUES
		('KILLA','KILLB','gate','binance',true,false,'pnl闸',100,'manual_seed','fixed',-50,NULL),
		('KILLC','KILLD','gate','binance',true,false,'连败闸',100,'manual_seed','fixed',NULL,2),
		('KILLE','KILLF','gate','binance',true,false,'无判据',100,'manual_seed','fixed',NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	// 持仓+结算链：KILLA 组合仓 3 笔净亏结算（累计 -80 < -50 阈+连败 3）；
	// KILLC 组合仓 2 笔连续净亏（连败 2 达阈）
	// 一仓一结算（pkey=position_id）——连败=同组合多仓位连续净亏
	newPosKill := func(oppID, cb, cs string, net float64, ageDays int) {
		var pid int64
		if err := st.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1,'XASSET_CARRY','CLOSED',now()) RETURNING id`,
			oppID).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool().Exec(ctx, `
			INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, status)
			VALUES ($1,1,'OPEN','gate','perp',$2,'BUY',1,100,'FILLED'),
			       ($1,2,'OPEN','binance','perp',$3,'SELL',1,100,'FILLED')`, pid, cb, cs); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool().Exec(ctx, `
			INSERT INTO position_settlements (position_id, settled_at, net_realized_usd, incident_attributed)
			VALUES ($1, now() - interval '1 day' * $2, $3, false)`, pid, ageDays, net); err != nil {
			t.Fatal(err)
		}
	}
	// KILLA：3 仓净亏累计 -80<-50 阈（连败 3）；KILLC：2 仓连续净亏（连败 2 达阈）
	newPosKill("kill-pnl-1", "KILLA", "KILLB", -30, 3)
	newPosKill("kill-pnl-2", "KILLA", "KILLB", -25, 2)
	newPosKill("kill-pnl-3", "KILLA", "KILLB", -25, 1)
	newPosKill("kill-streak-1", "KILLC", "KILLD", -10, 2)
	newPosKill("kill-streak-2", "KILLC", "KILLD", -5, 1)
	// KILLG：连败闸阈 2——最新=正结算夹断（rn1=+5 断计数→leading=0 不摘）
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_sell,
			enabled, simulated, note, max_gross_usd, source, sizing_mode, kill_loss_streak)
		VALUES ('KILLG','KILLH','gate','binance',true,false,'夹断',100,'manual_seed','fixed',2)`); err != nil {
		t.Fatal(err)
	}
	newPosKill("kill-break-old", "KILLG", "KILLH", -20, 3)
	newPosKill("kill-break-mid", "KILLG", "KILLH", -15, 2)
	newPosKill("kill-break-new", "KILLG", "KILLH", +5, 1) // rn1 非亏→leading=0

	n, err := st.DisableKillTriggeredCombos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 { // KILLA(pnl)+KILLC(连败)；KILLG 夹断不摘
		t.Fatalf("kill 扳机摘=%d want 2（pnl 闸+连败闸）", n)
	}
	for _, tc := range []struct {
		cb   string
		want bool
		name string
	}{
		{"KILLA", false, "净 PnL 超阈摘"},
		{"KILLC", false, "连败达阈摘"},
		{"KILLE", true, "无判据不摘"},
		{"KILLG", true, "夹断连败不摘（leading negatives）"},
	} {
		var en bool
		if err := st.Pool().QueryRow(ctx,
			`SELECT enabled FROM xasset_allow_combos WHERE canonical_buy=$1`, tc.cb).Scan(&en); err != nil {
			t.Fatal(err)
		}
		if en != tc.want {
			t.Fatalf("%s: enabled=%v want %v", tc.name, en, tc.want)
		}
	}
}

// TestMtIODeltaRound D-602 锚：差分落账——注资残差>0.01 记 deposit 行/
// PnL 抵扣残差~0 不记/幂等键 mt5:delta 不重。
func TestMtIODeltaWindowsAndClosedPnL(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	// 快照链：1000→2000（Δ=+1000 注资）→2100（Δ=+100 PnL 抵扣~0 不记）
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO fin_venue_account_snapshot (venue, venue_type, balance_raw, fetched_at, src)
		VALUES ('110660304','mt5',1000,now()-interval '3 hours','test'),
		       ('110660304','mt5',2000,now()-interval '2 hours','test'),
		       ('110660304','mt5',2100,now()-interval '1 hour','test')`); err != nil {
		t.Fatal(err)
	}
	snaps, err := st.BalanceDeltaWindows(ctx, "110660304", time.Now().Add(-24*time.Hour))
	if err != nil || len(snaps) != 3 {
		t.Fatalf("snaps=%d err=%v", len(snaps), err)
	}
	// 关仓 PnL +100 在窗 2（抵扣后残差~0）
	var pid int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl)
		VALUES ('pnl-test','XASSET_CARRY','CLOSED',now()-interval '4 hours',now()-interval '90 minutes',100) RETURNING id`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, status)
		VALUES ($1,1,'OPEN','110660304','mt5','XMRUSD','BUY',1,100,'FILLED')`, pid); err != nil {
		t.Fatal(err)
	}
	pnl, err := st.ClosedPositionPnLBetween(ctx, "110660304", snaps[1].Ts.Add(-time.Minute), snaps[2].Ts.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if pnl != 100 {
		t.Fatalf("closed pnl=%v want 100", pnl)
	}
	// 残差判据重演：窗1 Δ=+1000→残差 +1000-0=+1000>0.01 deposit；
	// 窗2 Δ=+100→残差 100-100=0<0.01 不记
	d1 := snaps[1].Balance - snaps[0].Balance // +1000
	d2 := snaps[2].Balance - snaps[1].Balance // +100
	if d1-0 <= 0.01 {
		t.Fatalf("窗1残差=%v 应>0.01", d1)
	}
	if d2-pnl >= 0.01 {
		t.Fatalf("窗2残差=%v 应<0.01", d2-pnl)
	}
}
