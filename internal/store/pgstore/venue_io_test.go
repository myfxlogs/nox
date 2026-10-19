// venue_io_test.go —— D-365 S5：fin_venue_io 幂等+水位+Recent 锚。
package pgstore

import (
	"context"
	"testing"
	"time"
)

func TestVenueIO(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE fin_venue_io`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ts := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	fee := 1.5
	rows := []VenueIO{
		{Venue: "binance", ConfigID: 20, Direction: "deposit", Ccy: "USDT", Amount: 22000,
			Status: "1", TxID: "0xabc", ExternalID: "5232145709440380673", Ts: ts},
		{Venue: "binance", ConfigID: 20, Direction: "withdraw", Ccy: "TRX", Amount: 379.0191,
			Fee: &fee, Status: "6", TxID: "0xdef", ExternalID: "999", Ts: ts.Add(time.Hour)},
		{Venue: "okx", ConfigID: 30, Direction: "deposit", Ccy: "USDT", Amount: 400,
			Status: "2", ExternalID: "431879636", Ts: ts.Add(2 * time.Hour)},
	}
	n, err := s.InsertVenueIO(ctx, rows)
	if err != nil || n != 3 {
		t.Fatalf("insert = %d (err=%v), want 3", n, err)
	}
	// 幂等：同行重插 → 0 新增
	n, err = s.InsertVenueIO(ctx, rows)
	if err != nil || n != 0 {
		t.Fatalf("re-insert = %d (err=%v), want 0（幂等）", n, err)
	}
	// 水位游标：deposit 方向 = ts（第一行）
	wm, ok, err := s.LatestVenueIOTs(ctx, 20, "deposit")
	if err != nil || !ok || !wm.Equal(ts) {
		t.Fatalf("watermark deposit = %v ok=%v err=%v, want %v", wm, ok, err, ts)
	}
	wm, ok, _ = s.LatestVenueIOTs(ctx, 20, "withdraw")
	if !ok || !wm.Equal(ts.Add(time.Hour)) {
		t.Fatalf("watermark withdraw = %v, want +1h", wm)
	}
	if _, ok, _ := s.LatestVenueIOTs(ctx, 99, "deposit"); ok {
		t.Error("空配置水位应 ok=false")
	}
	// Recent：config 20 两行 ts 倒序
	rec, err := s.RecentVenueIO(ctx, "binance", 20, 10)
	if err != nil || len(rec) != 2 {
		t.Fatalf("recent = %d (err=%v), want 2", len(rec), err)
	}
	if rec[0].Direction != "withdraw" || rec[1].Direction != "deposit" {
		t.Errorf("recent 顺序应 ts 倒序: %s then %s", rec[0].Direction, rec[1].Direction)
	}
	if rec[0].Fee == nil || *rec[0].Fee != 1.5 {
		t.Errorf("withdraw fee want 1.5, got %v", rec[0].Fee)
	}
}

// TestVenueIOTxIDDedup D-607 退回㉘端到端锚：deal 行先落（txid=order:{n}）→
// ticket 扫描同笔查 txid 命中→跳过（fin_venue_io 同笔入金仅 1 行）。
func TestVenueIOTxIDDedup(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	// config_id 占位（broker_configs 库行无关——查重键=config_id+txid）
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO fin_venue_io (venue, config_id, direction, ccy, amount, txid, external_id, ts)
		VALUES ('110660304', 9901, 'deposit', 'USD', 1000, 'order:888', 'mt5:deal:999', now())`); err != nil {
		t.Fatal(err)
	}
	// deal 已落→ticket 扫描查 txid='order:888' 命中→应跳过（同笔不双记）
	ok, err := st.VenueIOTxIDExists(ctx, 9901, "order:888")
	if err != nil || !ok {
		t.Fatalf("txid 查重应命中：%v %v", ok, err)
	}
	// 异 txid 不误中
	ok, err = st.VenueIOTxIDExists(ctx, 9901, "order:777")
	if err != nil || ok {
		t.Fatalf("异 txid 误中：%v %v", ok, err)
	}
	// external_id 向查同径（deal→ticket 反向）
	ok, err = st.VenueIOExternalExists(ctx, 9901, "mt5:deal:999")
	if err != nil || !ok {
		t.Fatalf("external_id 查重应命中：%v %v", ok, err)
	}
}

// TestVenueIOEarnDualWriteDedup earn-io 专项验收锚①：执行侧手写行×采集器
// se-* 行同 external_id 域（键域对齐后）撞键行为钉死——同键第二插=幂等吞
// （禁静默双记），异键异事=各行独立在册（禁误吞）。
func TestVenueIOEarnDualWriteDedup(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `DELETE FROM fin_venue_io WHERE venue='bndedup'`); err != nil {
		t.Fatal(err)
	}
	// 执行侧申购行：external_id=se-<purchaseId>（键域对齐后径）
	execRow := VenueIO{Venue: "bndedup", ConfigID: 9902, Direction: "transfer", Ccy: "USDT",
		Amount: 100.5, Status: "CONFIRMED", ExternalID: "se-882211", Ts: time.Now(),
		FromWallet: "spot", ToWallet: "earn"}
	n, err := st.InsertVenueIO(ctx, []VenueIO{execRow})
	if err != nil || n != 1 {
		t.Fatalf("执行侧首插应成: n=%d err=%v", n, err)
	}
	// 采集器同事件行（同 se- 键——史回放到场）→ 撞键幂等吞不双记
	collRow := execRow
	collRow.Status = "SUCCESS" // 采集器原生状态域
	n, err = st.InsertVenueIO(ctx, []VenueIO{collRow})
	if err != nil || n != 0 {
		t.Fatalf("同键采集行应幂等吞: n=%d err=%v", n, err)
	}
	// 异键异事件不误吞（se-882212 独立成账）
	n, err = st.InsertVenueIO(ctx, []VenueIO{{Venue: "bndedup", ConfigID: 9902, Direction: "transfer",
		Ccy: "USDT", Amount: 7.25, Status: "SUCCESS", ExternalID: "se-882212", Ts: time.Now(),
		FromWallet: "earn", ToWallet: "spot"}})
	if err != nil || n != 1 {
		t.Fatalf("异键异事件应独立在册: n=%d err=%v", n, err)
	}
	var cnt int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_venue_io WHERE venue='bndedup'`).Scan(&cnt); err != nil || cnt != 2 {
		t.Fatalf("台账行数应=2（撞键吞+异键留）: %d %v", cnt, err)
	}
}
