// d580_waitreason_test.go —— D-580 锚：待入场等待条件透出（等什么+当前多少+还差多少）。
// A1 条件透出（qbus 双腿+monitor 阈值→人话整串+三字段齐）/A2 同源（展示值与决策面
// 同公式同 qbus 键，数值逐位断言）/A3 数据缺诚实（报价缺/monitor 无行→留空+后缀不猜）。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/data/bus"
	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/platform/testutil"
	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"
)

// d580ReadySvc 造「七闸全过」的 XASSET_CARRY 组合 905（MT 买腿×perp 卖腿正向）。
// 返回 svc（qbus 已接线未发价）与 store；数据面：allow_rules+funding facts+ticks+
// monitor 行+快照+MT 保证金率 fact+perp 杠杆缝全就位，仅差 qbus 报价由用例自喂。
func d580ReadySvc(t *testing.T) *Service {
	t.Helper()
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	testutil.EnsureTicksPartition(t, st) // 全新席位库迁移只建历史月分区——闸 4 ticks now() 插入需当月（D-016 R1）
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE xasset_allow_combos, autotrade_allow_rules,
		fin_venue_symbol_flags, fin_venue_account_snapshot, positions, position_legs,
		facts, ticks, fin_xasset_monitor RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	svc := &Service{store: st, qbus: bus.New(), tierState: map[string]tierState{}}
	svc.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) { return 5, nil }
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM xasset_allow_combos WHERE id=905;
			DELETE FROM autotrade_allow_rules WHERE value IN ('3001157:mt5','gate:perp');
			DELETE FROM facts WHERE src='d580test';
			DELETE FROM ticks WHERE symbol IN ('XPTUSD','XPTUSDT') AND venue IN ('3001157','gate');
			DELETE FROM fin_xasset_monitor WHERE pair='XPTUSDT-XPT' AND cex_venue='gate:perp';
			DELETE FROM fin_venue_account_snapshot WHERE venue IN ('3001157','gate') AND src='d580test'`)
	})

	// 闸 1：venue 放行。
	if _, err := st.Pool().Exec(ctx, `INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated)
		VALUES ('venue','3001157:mt5',true,false),('venue','gate:perp',true,false)`); err != nil {
		t.Fatal(err)
	}
	// 闸 3：funding 收入腿 24h 窗 ≥12 样本且 worst≥floor（gate:perp 卖腿值 5.0 > 2.0）。
	for i := 0; i < 13; i++ {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
			 VALUES ('funding','gate','XPTUSDT',5.0,'pct_annualized',$1,'d580test')`,
			time.Now().Add(-time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	// MT 腿保证金率 fact（≤72h 新鲜；0.04=25× 杠杆）。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
		 VALUES ('init_margin_rate','3001157',NULL,0.04,'ratio',now(),'d580test')`); err != nil {
		t.Fatal(err)
	}
	// 闸 4：双腿 tick 新鲜。
	if _, err := st.Pool().Exec(ctx, `INSERT INTO ticks (ts, venue, symbol, bid, ask, venue_type)
		VALUES (now(),'3001157','XPTUSD',1999,2000,'mt5'),(now(),'gate','XPTUSDT',2002.06,2003,'perp')`); err != nil {
		t.Fatal(err)
	}
	// 闸 5+阈值：monitor 行 noise=18.8（D-580 阈值源同 LatestXAssetEntryGate）。
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_xasset_monitor
		(pair, cex_venue, mt_venue, binance_bid, binance_ask, exness_bid, exness_ask,
		 entry_spread_bps, exit_spread_bps, entry_net_bps, funding_rate_8h,
		 funding_annual_pct, tracking_error_bps, equiv_annual_pct, noise_bps)
		VALUES ('XPTUSDT-XPT','gate:perp','3001157:mt5',2002.06,2003,1999,2000,
		 10.3,5,3,0.001,21.9,1.0,24.9,18.8)`); err != nil {
		t.Fatal(err)
	}
	// 腿池资金（闸用 equity；notional 100 << 5000）——#1c：实时态种子。
	v5k := 5000.0
	seedLiveAccounts(t, svc, []pgstore.VenueAccountLatest{
		{Venue: "gate", VenueType: "perp", BalanceRaw: &v5k, EquityRaw: &v5k, FreeMarginRaw: &v5k, Currency: "USDT", FetchedAt: time.Now()},
		{Venue: "3001157", VenueType: "mt5", BalanceRaw: &v5k, EquityRaw: &v5k, FreeMarginRaw: &v5k, Currency: "USDT", FetchedAt: time.Now()},
	})
	// D-007 S2：闸6 死键修复后 venue cap 真实生效——夹具抬闸防误拦（共享库或带历史种子行）。
	// 共享库：Cleanup 还原前值（无则删），防 gate:perp=100000 之类残值污染 D543 种子断言。
	type capRow struct {
		key     string
		existed bool
		capUSD  float64
		enabled bool
	}
	var saved []capRow
	for _, k := range []string{"3001157:mt5", "gate:perp"} {
		var r capRow
		r.key = k
		err := st.Pool().QueryRow(ctx, `SELECT cap_usd, enabled FROM fin_venue_exposure_caps WHERE venue_key=$1`, k).Scan(&r.capUSD, &r.enabled)
		r.existed = err == nil
		saved = append(saved, r)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_venue_exposure_caps (venue_key, cap_usd, enabled, note)
		VALUES ('3001157:mt5',100000,true,'d580test'),('gate:perp',100000,true,'d580test')
		ON CONFLICT (venue_key) DO UPDATE SET cap_usd=100000, enabled=true`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, r := range saved {
			if r.existed {
				_, _ = st.Pool().Exec(context.Background(), `UPDATE fin_venue_exposure_caps SET cap_usd=$2, enabled=$3 WHERE venue_key=$1`, r.key, r.capUSD, r.enabled)
			} else {
				_, _ = st.Pool().Exec(context.Background(), `DELETE FROM fin_venue_exposure_caps WHERE venue_key=$1`, r.key)
			}
		}
	})
	// enabled 组合行（notional=100）。
	n100 := 100.0
	d478ComboLegs(t, st, 905, true, &n100, 1000, "XPTUSD", "XPTUSDT")
	return svc
}

// d580Publish 双腿 qbus 报价（mtAsk=MT 腿卖价、cexBid=CEX 腿买价——同 engine 取用侧）。
func d580Publish(svc *Service, mtAsk, cexBid float64, age time.Duration) {
	now := time.Now().Add(-age)
	svc.qbus.Publish(bus.Quote{Symbol: "XPTUSD", Venue: "3001157", VenueType: "mt5",
		Bid: mtAsk - 1, Ask: mtAsk, Time: now, VenueSymbol: "XPTUSD"})
	svc.qbus.Publish(bus.Quote{Symbol: "XPTUSDT", Venue: "gate", VenueType: "perp",
		Bid: cexBid, Ask: cexBid + 1, Time: now, VenueSymbol: "XPTUSDT"})
}

func d580Row(t *testing.T, svc *Service) *v1.XAssetPendingEntryRow {
	t.Helper()
	rows, err := svc.pendingEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ComboId == 905 {
			return r
		}
	}
	t.Fatal("905 行缺失")
	return nil
}

// A1+A2：双腿报价+monitor 阈值就位 → 人话整串含阈值/当前/还差+三字段齐；
// 数值逐位断言（cexBid 2002.06−mtAsk 2000)/2000×1e4=10.3bps，noise 18.8 → 还差 8.5。
func TestD580_WaitConditionExposed(t *testing.T) {
	svc := d580ReadySvc(t)
	d580Publish(svc, 2000, 2002.06, 0)
	row := d580Row(t, svc)
	if !row.Ready {
		t.Fatalf("闸未全过: reason=%q", row.WaitReason)
	}
	for _, want := range []string{"入场价差>18.8", "当前10.3", "还差8.5bps"} { // D-587 坑3 短格式断言
		if !strings.Contains(row.WaitReason, want) {
			t.Fatalf("WaitReason 缺 %q: %q", want, row.WaitReason)
		}
	}
	if row.WaitCurrentBps == nil || *row.WaitCurrentBps < 10.29 || *row.WaitCurrentBps > 10.31 ||
		row.WaitThresholdBps == nil || *row.WaitThresholdBps != 18.8 ||
		row.WaitGapBps == nil || *row.WaitGapBps < 8.49 || *row.WaitGapBps > 8.51 {
		t.Fatalf("三字段: cur=%v thr=%v gap=%v", row.WaitCurrentBps, row.WaitThresholdBps, row.WaitGapBps)
	}
}

// A3：数据缺诚实——报价缺/报价超 5min/monitor 无行 → 三字段留空+「（实况价差不可得：<因>）」
// （D-006 S4 带因升级：黑话后缀升级为可排障因串）。
func TestD580_WaitConditionDataMissing(t *testing.T) {
	t.Run("报价缺", func(t *testing.T) {
		svc := d580ReadySvc(t)
		row := d580Row(t, svc)
		if !row.Ready || !strings.Contains(row.WaitReason, "实况价差不可得：") ||
			!strings.Contains(row.WaitReason, "qbus未接") {
			t.Fatalf("reason=%q ready=%v", row.WaitReason, row.Ready)
		}
		if row.WaitCurrentBps != nil || row.WaitThresholdBps != nil || row.WaitGapBps != nil {
			t.Fatal("报价缺三字段应留空")
		}
	})
	t.Run("报价超龄", func(t *testing.T) {
		svc := d580ReadySvc(t)
		d580Publish(svc, 2000, 2002.06, 6*time.Minute)
		row := d580Row(t, svc)
		if !strings.Contains(row.WaitReason, "实况价差不可得：") || !strings.Contains(row.WaitReason, "报价超龄") ||
			row.WaitCurrentBps != nil {
			t.Fatalf("超龄应留空+带因后缀: %q", row.WaitReason)
		}
	})
	t.Run("monitor无行", func(t *testing.T) {
		svc := d580ReadySvc(t)
		if _, err := svc.store.Pool().Exec(context.Background(),
			`DELETE FROM fin_xasset_monitor WHERE pair='XPTUSDT-XPT'`); err != nil {
			t.Fatal(err)
		}
		d580Publish(svc, 2000, 2002.06, 0)
		// monitor 无行 → 闸 5 拦截（监控数据过期）——非等待态，诚实透出闸原因不猜。
		row := d580Row(t, svc)
		if row.Ready || !strings.Contains(row.WaitReason, "监控数据过期") {
			t.Fatalf("reason=%q ready=%v", row.WaitReason, row.Ready)
		}
		if row.WaitCurrentBps != nil {
			t.Fatal("非等待态三字段应留空")
		}
	})
}

// D-587 坑4：:246「条件就绪·等机会」黑话按 kind 分流断言——
// CARRY=等价差达阈（报价缺分支保留此前缀+诚实后缀）/ FUNDING_HEDGE=等净息差达阈。
func TestD587_ReadyWaitKindText(t *testing.T) {
	// CARRY：报价缺 → fillWaitCondition 前缀仍是分流后文案。
	t.Run("XASSET_CARRY", func(t *testing.T) {
		svc := d580ReadySvc(t)
		row := d580Row(t, svc)
		if !row.Ready || !strings.HasPrefix(row.WaitReason, "条件就绪·等价差达阈") {
			t.Fatalf("reason=%q ready=%v", row.WaitReason, row.Ready)
		}
	})
	// FUNDING_HEDGE：spot 买×perp 卖组合 908 走全闸就绪 → 等净息差达阈。
	t.Run("FUNDING_HEDGE", func(t *testing.T) {
		svc := d580ReadySvc(t)
		st := svc.store
		ctx := context.Background()
		if _, err := st.Pool().Exec(ctx, `INSERT INTO xasset_allow_combos
			(id, canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell, max_long_qty, max_short_qty, max_gross_usd, notional_usd, enabled, simulated)
			VALUES (908,'FHSYM','FHSYM','binance','spot','gate','perp',1,1,1000,100,true,false)`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = st.Pool().Exec(ctx, `DELETE FROM xasset_allow_combos WHERE id=908;
				DELETE FROM autotrade_allow_rules WHERE value='binance:spot';
				DELETE FROM facts WHERE src='d587test';
				DELETE FROM ticks WHERE symbol='FHSYM';
				DELETE FROM fin_venue_account_snapshot WHERE venue='binance' AND src='d587test'`)
		})
		// 闸 1：binance:spot 放行（gate:perp 夹具已种）。
		if _, err := st.Pool().Exec(ctx, `INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated)
			VALUES ('venue','binance:spot',true,false)`); err != nil {
			t.Fatal(err)
		}
		// 闸 3：FH regime 闸——FundingPredWindow(gate,FHSYM) ≥12 样本 worst≥2.0。
		for i := 0; i < 13; i++ {
			if _, err := st.Pool().Exec(ctx,
				`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
				 VALUES ('funding','gate','FHSYM',5.0,'pct_annualized',$1,'d587test')`,
				time.Now().Add(-time.Duration(i)*time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		// 闸 4：双腿 tick 新鲜（binance spot + gate perp，symbol=canonical）。
		if _, err := st.Pool().Exec(ctx, `INSERT INTO ticks (ts, venue, symbol, bid, ask, venue_type)
			VALUES (now(),'binance','FHSYM',100,100.1,'spot'),(now(),'gate','FHSYM',100,100.1,'perp')`); err != nil {
			t.Fatal(err)
		}
		// 腿池：binance spot 实时态种子（gate/3001157 夹具已种；#1c 实时源=acctMon）。
		v5k2 := 5000.0
		seedLiveAccounts(t, svc, []pgstore.VenueAccountLatest{
			{Venue: "binance", VenueType: "spot", BalanceRaw: &v5k2, EquityRaw: &v5k2, FreeMarginRaw: &v5k2, Currency: "USDT", FetchedAt: time.Now()},
		})
		rows, err := svc.pendingEntries(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var row *v1.XAssetPendingEntryRow
		for _, r := range rows {
			if r.ComboId == 908 {
				row = r
			}
		}
		if row == nil {
			t.Fatal("908 行缺失")
		}
		if row.EntryKind != "FUNDING_HEDGE" || !row.Ready || row.WaitReason != "条件就绪·等净息差达阈" {
			t.Fatalf("kind=%q ready=%v reason=%q", row.EntryKind, row.Ready, row.WaitReason)
		}
	})
}
