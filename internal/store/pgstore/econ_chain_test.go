// econ_chain_test.go —— 消化链建制锚：①裁定枚举落库校验 ②负结算裁定消费点
// （dismiss 同因重报=病灶信号）③#20 回放——combo 底层一致性三源互证闸回归。
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEconVerdict_CRUD(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	// 理由必填（dismiss 垃圾桶防线）
	if err := st.InsertEconVerdict(ctx, EconVerdict{EventKey: "ek-v1", Verdict: "dismiss"}); err == nil {
		t.Fatal("dismiss 无理由应拒")
	}
	// canon 须 entry+fixpoint（无回改点不入库）
	if err := st.InsertEconVerdict(ctx, EconVerdict{EventKey: "ek-v2", Verdict: "canon", Reason: "r", EntryID: "e1"}); err == nil {
		t.Fatal("canon 无 fixpoint 应拒")
	}
	if err := st.InsertEconVerdict(ctx, EconVerdict{EventKey: "ek-v3", Verdict: "canon", Reason: "r", Fixpoint: "eval:x"}); err == nil {
		t.Fatal("canon 无 entry 应拒")
	}
	// 未知枚举拒
	if err := st.InsertEconVerdict(ctx, EconVerdict{EventKey: "ek-v4", Verdict: "bogus", Reason: "r"}); err == nil {
		t.Fatal("未知裁定枚举应拒")
	}
	// 三出路各落一行 + 撞键幂等错
	for _, v := range []EconVerdict{
		{EventKey: "ek-c1", Verdict: "canon", Reason: "转正", EntryID: "e-canon", Fixpoint: "combo_gate:underlying-consistency"},
		{EventKey: "ek-d1", Verdict: "dismiss", Reason: "一次性噪声"},
		{EventKey: "ek-m1", Verdict: "merge", Reason: "并入", EntryID: "e-target"},
	} {
		if err := st.InsertEconVerdict(ctx, v); err != nil {
			t.Fatalf("%s: %v", v.Verdict, err)
		}
	}
	if err := st.InsertEconVerdict(ctx, EconVerdict{EventKey: "ek-c1", Verdict: "dismiss", Reason: "x"}); !errors.Is(err, ErrEconVerdictExists) {
		t.Fatalf("撞键应 ErrEconVerdictExists: %v", err)
	}
	v, ok, err := st.EconVerdictByKey(ctx, "ek-c1")
	if err != nil || !ok || v.Verdict != "canon" || v.EntryID != "e-canon" || v.Fixpoint == "" {
		t.Fatalf("回读裁定: %+v ok=%v err=%v", v, ok, err)
	}
	if _, ok, _ := st.EconVerdictByKey(ctx, "ek-none"); ok {
		t.Fatal("未裁键不应 ok")
	}
}

// 负结算接线裁定消费点：dismiss 重报→warn 病灶信号+不产行；canon→静默不产行。
func TestEconPending_VerdictConsumed(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("NOX_ECONKB_DIR", dir)
	seed := func(opp, key string, net float64) int64 {
		var pid int64
		if err := st.pool.QueryRow(ctx, `INSERT INTO positions (opportunity_id,type,status,opened_at,closed_at,realized_pnl)
			VALUES ($1,'XASSET_CARRY','CLOSED',now(),now(),$2) RETURNING id`, opp, net).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if _, err := st.pool.Exec(ctx, `INSERT INTO position_settlements
			(position_id,vkey,expected_usd,realized_usd,settled_at,net_realized_usd,incident_attributed,settlement_event_key)
			VALUES ($1,'XASSET:EA_V',10,$2,now(),$2,false,$3)`, pid, net, key); err != nil {
			t.Fatal(err)
		}
		return pid
	}
	pidD := seed("opp-vd", "ek-dis-1", -2.5)
	pidC := seed("opp-vc", "ek-can-1", -4.0)
	if err := st.InsertEconVerdict(ctx, EconVerdict{EventKey: "ek-dis-1", Verdict: "dismiss", Reason: "测一次性"}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertEconVerdict(ctx, EconVerdict{EventKey: "ek-can-1", Verdict: "canon", Reason: "转正", EntryID: "e-x", Fixpoint: "eval:x"}); err != nil {
		t.Fatal(err)
	}
	st.econPendingForSettlement(ctx, pidD)
	st.econPendingForSettlement(ctx, pidC)
	b, _ := os.ReadFile(filepath.Join(dir, econPendingName))
	if strings.Contains(string(b), "ek-dis-1") || strings.Contains(string(b), "ek-can-1") {
		t.Fatalf("已裁键不应再产待归纳行:\n%s", b)
	}
	// dismiss 同因重报→warn 病灶信号行
	var n int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE level='warn' AND message LIKE '%dismiss 同因重报%ek-dis-1%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("dismiss 重报应产 warn 病灶信号: %d", n)
	}
}

// EconPendingBacklog 积压源（周报消化态列+digest 闸共用）。
func TestEconPendingBacklog(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	b0, _, err := st.EconPendingBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pid int64
	if err := st.pool.QueryRow(ctx, `INSERT INTO positions (opportunity_id,type,status,opened_at,closed_at,realized_pnl)
		VALUES ('opp-bl','XASSET_CARRY','CLOSED',now(),now(),-1) RETURNING id`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO position_settlements
		(position_id,vkey,expected_usd,realized_usd,settled_at,net_realized_usd,incident_attributed,settlement_event_key)
		VALUES ($1,'XASSET:EA_BL',10,-9,now()-interval '5 days',-9,false,'ek-bl-1')`, pid); err != nil {
		t.Fatal(err)
	}
	rows, oldest, err := st.EconPendingBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows != b0+1 || oldest < 4 {
		t.Fatalf("backlog rows=%d oldest=%d (want %d/≥4)", rows, oldest, b0+1)
	}
	if err := st.InsertEconVerdict(ctx, EconVerdict{EventKey: "ek-bl-1", Verdict: "dismiss", Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	rows2, _, _ := st.EconPendingBacklog(ctx)
	if rows2 != b0 {
		t.Fatalf("裁定后积压应回 %d: %d", b0, rows2)
	}
}

// #20 回放——底层一致性三源互证：SPCXUSD1(hedge=SPX500) × 幻影 US500 必拦；
// 规范化后 SPX500 放行；类互斥/价位背离同拦（回归断言=历史错配行回放必拦）。
func TestComboUnderlyingGate_Replay20(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	t.Setenv("NOX_ECONKB_DIR", t.TempDir()) // 坑签名闸隔离（空库零签名）
	now := time.Now()
	if err := st.UpsertCexUniverse(ctx, []CexUniverseRow{
		{Venue: "binance", Symbol: "SPCXUSD1", Base: "SPX", Class: "equity_index", Status: "active", HedgeCanonical: "SPX500"},
		{Venue: "binance", Symbol: "SPCXUSDT", Base: "SPX", Class: "equity_index", Status: "active", HedgeCanonical: "SPX500"},
		{Venue: "gate", Symbol: "AAAUSDT", Base: "AAA", Class: "alt", Status: "active", HedgeCanonical: "AAA"},
		{Venue: "gate", Symbol: "BBBUSDT", Base: "BBB", Class: "alt", Status: "active", HedgeCanonical: "BBB"},
	}, now); err != nil {
		t.Fatal(err)
	}
	for _, e := range []AssetEquivEntry{
		{AssetClass: "SPX500", Canonical: "SPCXUSDT", VenueType: "perp", Venue: "binance"},
		{AssetClass: "SPX500", Canonical: "SPCXUSD1", VenueType: "perp", Venue: "binance"},
		{AssetClass: "SPX500", Canonical: "SPX500", VenueType: "mt5", Venue: "15032313"},
		{AssetClass: "AAA", Canonical: "AAAUSDT", VenueType: "perp", Venue: "gate"},
		{AssetClass: "BBB", Canonical: "BBBUSDT", VenueType: "perp", Venue: "gate"},
	} {
		if _, err := st.InsertAssetEquivIfAbsent(ctx, &e); err != nil {
			t.Fatal(err)
		}
	}
	combo := func(cb, cs, vb, vs string) error {
		_, err := st.InsertXAssetAllowCombo(ctx, &XAssetComboInsert{
			CanonicalBuy: cb, CanonicalSell: cs, VenueBuy: vb, VenueSell: vs,
			MaxGrossUSD: 100, MaxLongQty: 1, MaxShortQty: 1, Source: "s2_trial",
		})
		return err
	}
	// ①#20 原始错配回放：hedge=SPX500 × 幻影 US500（不在等价类）→拦
	if err := combo("SPCXUSD1", "US500", "binance:perp", "15032313:mt5"); err == nil ||
		!strings.Contains(err.Error(), "底层一致性拒") {
		t.Fatalf("#20 回放应拦: %v", err)
	}
	// ②规范化后 SPCXUSD1×SPX500（hedge==canonical）→放行
	if err := combo("SPCXUSD1", "SPX500", "binance:perp", "15032313:mt5"); err != nil {
		t.Fatalf("同身份配对应放行: %v", err)
	}
	// ③类互斥回放：AAA 类 × BBB 类（各自 hedge 非空且互不相符）→拦
	if err := combo("AAAUSDT", "BBBUSDT", "gate:perp", "gate:perp"); err == nil {
		t.Fatal("类互斥配对应拦")
	}
	// ④价位量级背离隔离锚：双腿无 universe/equiv 行（前两源弃权）+ticks mid
	// 比 >4 →拦（判③独立生效非前两源代拦）。分区表先建当月分区（facts_test 先例）。
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	next := first.AddDate(0, 1, 0)
	if _, err := st.pool.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s PARTITION OF ticks FOR VALUES FROM ('%s') TO ('%s')`,
		fmt.Sprintf("ticks_%d_%02d", now.Year(), now.Month()),
		first.Format("2006-01-02"), next.Format("2006-01-02"))); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO ticks (ts, venue, venue_type, symbol, bid, ask) VALUES
		(now(),'gate','perp','CCCUSDT',100,101),
		(now(),'gate','perp','DDDUSDT',0.5,0.51)`); err != nil {
		t.Fatal(err)
	}
	if err := combo("CCCUSDT", "DDDUSDT", "gate:perp", "gate:perp"); err == nil ||
		!strings.Contains(err.Error(), "量级背离") {
		t.Fatalf("价位量级背离应拦: %v", err)
	}
}
