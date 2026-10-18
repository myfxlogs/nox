package budget

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/platform/testutil/storetest"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// TestM44_Derive 纯函数推导锚：deployable=nav×(1−reserve)；per=min(maxNotional,均分)。
func TestM44_Derive(t *testing.T) {
	usd := map[string]pgstore.NavBucket{"USD": {Nav: 10000}}
	got := derive(usd, 30, 1000, []string{"SWAPARB", "MEANREVERT"}, gate.StrategySettleCcy)
	if got["SWAPARB"] != 1000 || got["MEANREVERT"] != 1000 { // 7000/2=3500>cap→1000
		t.Fatalf("cap 生效错: %v", got)
	}
	got = derive(map[string]pgstore.NavBucket{"USD": {Nav: 4000}}, 30, 1000,
		[]string{"A", "B", "C", "D"}, gate.StrategySettleCcy)
	if got["A"] != 700 { // 2800/4=700<cap→700（未注册名回退 USD 桶）
		t.Fatalf("均分生效错: %v", got)
	}
	if len(derive(usd, 30, 1000, nil, gate.StrategySettleCcy)) != 0 {
		t.Fatal("零 live 应空")
	}
	// D-522③：分币种 derive——USDT 策略吃 USDT 桶不吃 USD 桶。
	both := map[string]pgstore.NavBucket{"USD": {Nav: 10000}, "USDT": {Nav: 5000}}
	got = derive(both, 0, 1e9, []string{"SWAPARB", "cash_mgmt"}, gate.StrategySettleCcy)
	if got["SWAPARB"] != 10000 || got["cash_mgmt"] != 5000 {
		t.Fatalf("分币 derive 错: %v", got)
	}
	// USDT 桶缺席 → cash_mgmt 预算=0 行（无本不放预算）。
	got = derive(map[string]pgstore.NavBucket{"USD": {Nav: 10000}}, 0, 1e9,
		[]string{"cash_mgmt"}, gate.StrategySettleCcy)
	if got["cash_mgmt"] != 0 {
		t.Fatalf("缺席桶应零预算: %v", got)
	}
}

// TestM44_DailyCut 日切锚（真实测试库）：nav 行幂等 + live 策略预算行 + derived_from 留痕。
func TestM44_DailyCut(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE fin_nav_daily, fin_strategy_budget, fin_strategy,
		fin_venue_account_snapshot, fin_capability_constraint, autotrade_state, strategy_stats CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	defer func() {
		_, _ = st.Pool().Exec(ctx, `TRUNCATE fin_nav_daily, fin_strategy_budget, fin_strategy,
			fin_venue_account_snapshot, fin_capability_constraint, autotrade_state, strategy_stats CASCADE`)
	}()
	now := time.Now()
	// venue 快照两池（一 USD 一 EUR——EUR 池跳过不计，诚实口径锚）
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_venue_account_snapshot
		(venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at) VALUES
		('binance','perp',5000,6000,5000,'USD',$1),('okx','perp',3000,4000,3000,'USD',$1),
		('euracct','mt5',999,999,999,'EUR',$1)`, now); err != nil {
		t.Fatalf("seed snapshots: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO broker_accounts (venue, venue_type, host, port, password, is_real)
		VALUES ('euracct','mt5','h',443,'p',true) ON CONFLICT (venue) DO NOTHING`); err != nil {
		t.Fatalf("seed broker acct: %v", err)
	}
	defer func() { _, _ = st.Pool().Exec(ctx, `DELETE FROM broker_accounts WHERE venue='euracct'`) }()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_capability_constraint
		(scope, subject, qualifier, value_num, source) VALUES ('global','reserve_pct','min',30,'committee')`); err != nil {
		t.Fatalf("seed reserve: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_strategy (strategy, state) VALUES
		('SWAPARB','live'),('MEANREVERT','live'),('XASSET_CARRY','candidate')`); err != nil {
		t.Fatalf("seed fin_strategy: %v", err)
	}
	dayStart := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	if err := DailyCut(ctx, st, now, dayStart, 1000); err != nil {
		t.Fatalf("DailyCut: %v", err)
	}
	// nav=6000+4000=10000（USD 桶）；idle=8000；deployable=7000/2=3500→cap 1000
	// D-528：EUR 池不再跳过——独立 currency=EUR 行入 fin_nav_daily（跨币零加总）。
	rows, err := st.LatestNavDaily(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("nav 行错: %v %v", rows, err)
	}
	var row pgstore.NavDailyRow
	for _, r := range rows {
		if r.Currency == "EUR" && r.NavUSD != 999 {
			t.Fatalf("EUR 桶错（原生量零换算）: %+v", r)
		}
		if r.Currency == "USD" {
			row = r
		}
	}
	if row.Currency != "USD" {
		t.Fatalf("USD 行缺席: %v", rows)
	}
	if row.NavUSD != 10000 || row.IdleUSD != 8000 {
		t.Fatalf("nav/idle 错: %.0f/%.0f", row.NavUSD, row.IdleUSD)
	}
	for _, s := range []string{"SWAPARB", "MEANREVERT"} {
		b, ok, err := st.GetStrategyBudget(ctx, s)
		if err != nil || !ok {
			t.Fatalf("%s 预算行缺席: ok=%v err=%v", s, ok, err)
		}
		if b.BudgetUSD != 1000 {
			t.Fatalf("%s 预算错: %.0f", s, b.BudgetUSD)
		}
		if !strings.Contains(string(b.DerivedFrom), `"reserve_pct": 30`) {
			t.Fatalf("derived_from 缺痕: %s", b.DerivedFrom)
		}
	}
	if _, ok, _ := st.GetStrategyBudget(ctx, "XASSET_CARRY"); ok {
		t.Fatal("candidate 策略不该有预算行")
	}
	// 幂等：同日复算覆盖（nav 改值再切→行覆盖）
	if _, err := st.Pool().Exec(ctx, `DELETE FROM fin_venue_account_snapshot WHERE venue='okx'`); err != nil {
		t.Fatalf("del snapshot: %v", err)
	}
	if err := DailyCut(ctx, st, now, dayStart, 1000); err != nil {
		t.Fatalf("DailyCut 复跑: %v", err)
	}
	rows2, _ := st.LatestNavDaily(ctx)
	usd := 0.0
	for _, r := range rows2 {
		if r.Currency == "USD" {
			usd = r.NavUSD
		}
	}
	if len(rows2) != 2 || usd != 6000 {
		t.Fatalf("幂等覆盖错: %v", rows2)
	}
}

// TestM44_BudgetGate_Submit D-264 钦定验收锚：Submit 径预算闸——
// 行缺席→REJECTED；行在限额内→EXECUTED；超额→REJECTED。
func TestM44_BudgetGate_Submit(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE fin_strategy_budget, fin_strategy, autotrade_state,
		strategy_stats, autotrade_logs, positions, position_legs, position_cashflows, broker_accounts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	defer func() {
		_, _ = st.Pool().Exec(ctx, `TRUNCATE fin_strategy_budget, fin_strategy, autotrade_state,
			strategy_stats, autotrade_logs, positions, position_legs, position_cashflows, broker_accounts CASCADE`)
	}()
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO autotrade_state (state, reason) VALUES ('RUNNING','')`); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO fin_strategy (strategy, state) VALUES ('CROSS_VENUE','live')`); err != nil {
		t.Fatalf("seed live: %v", err)
	}
	insert := func(venue string) {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, is_real)
			 VALUES ($1,'mt5','h',443,'p','s','connected',false) ON CONFLICT DO NOTHING`, venue); err != nil {
			t.Fatalf("insert broker: %v", err)
		}
	}
	insert("vA")
	insert("vB")

	g := gate.NewGate(st, gate.Config{MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: false, MaxNotional: 100000}, nil)
	g.SetBudgetChecker(NewChecker(st))
	mk := func(notional float64) *gate.TradeIntent {
		return &gate.TradeIntent{
			Strategy: "CROSS_VENUE", Canonical: "AUDJPY", VenueBuy: "vA:mt5", VenueSell: "vB:mt5",
			Qty: 0.01, NotionalUSD: notional, DedupKey: "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
			Execute: func(context.Context) (string, string, int64) { return "EXECUTED", "", 1 },
		}
	}

	// ① 行缺席 → REJECTED fail-closed
	res := g.Submit(ctx, mk(500))
	if res.Status != "REJECTED" || !strings.Contains(res.Reason, "无预算行") {
		t.Fatalf("缺席应拒: %s %s", res.Status, res.Reason)
	}
	// ② 行在限额内 → EXECUTED
	if err := st.UpsertStrategyBudget(ctx, pgstore.StrategyBudget{
		Strategy: "CROSS_VENUE", BudgetUSD: 5000, DerivedFrom: []byte(`{}`), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed budget: %v", err)
	}
	res = g.Submit(ctx, mk(4000))
	if res.Status != "EXECUTED" {
		t.Fatalf("限额内应放行: %s %s", res.Status, res.Reason)
	}
	// ③ 超额 → REJECTED 带面额
	res = g.Submit(ctx, mk(9000))
	if res.Status != "REJECTED" || !strings.Contains(res.Reason, "预算") {
		t.Fatalf("超额应拒: %s %s", res.Status, res.Reason)
	}
}

// TestD522_NavCcySplit 分账锚：USDT 池归 USDT 桶——nav_daily 双行、earn 提案预算
// 吃 USDT 桶、USD 策略不被稀释（跨币永不加总）。
func TestD522_NavCcySplit(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE fin_nav_daily, fin_strategy_budget, fin_strategy,
		fin_venue_account_snapshot, fin_capability_constraint, autotrade_state, strategy_stats CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	defer func() {
		_, _ = st.Pool().Exec(ctx, `TRUNCATE fin_nav_daily, fin_strategy_budget, fin_strategy,
			fin_venue_account_snapshot, fin_capability_constraint, autotrade_state, strategy_stats CASCADE`)
	}()
	now := time.Now()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_venue_account_snapshot
		(venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at) VALUES
		('mtacct','mt5',4247,4247,4247,'USD',$1),('okx','unified',7794,7794,7794,'USDT',$1)`, now); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO broker_accounts (venue, venue_type, host, port, password, is_real)
		VALUES ('mtacct','mt5','h',443,'p',true) ON CONFLICT (venue) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = st.Pool().Exec(ctx, `DELETE FROM broker_accounts WHERE venue='mtacct'`) }()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_strategy (strategy, state) VALUES
		('XASSET_CARRY','live'),('cash_mgmt','live')`); err != nil {
		t.Fatal(err)
	}
	dayStart := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	if err := DailyCut(ctx, st, now, dayStart, 1e9); err != nil {
		t.Fatal(err)
	}
	rows, err := st.LatestNavDaily(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("应双行: %v %v", rows, err)
	}
	if rows[0].Currency != "USD" || rows[1].Currency != "USDT" ||
		rows[0].NavUSD != 4247 || rows[1].NavUSD != 7794 {
		t.Fatalf("分账行错: %+v", rows)
	}
	xb, _, _ := st.GetStrategyBudget(ctx, "XASSET_CARRY")
	cb, _, _ := st.GetStrategyBudget(ctx, "cash_mgmt")
	if xb == nil || cb == nil || xb.BudgetUSD != 4247 || cb.BudgetUSD != 7794 {
		t.Fatalf("预算错: xb=%v cb=%v", xb, cb)
	}
	if !strings.Contains(string(cb.DerivedFrom), `"ccy": "USDT"`) {
		t.Fatalf("derived_from 缺币种键: %s", cb.DerivedFrom)
	}
}

// TestDailyCut_LiveMapPerCcy NAV 指纹票回归锚：liveMap 键=venue:type:ccy——
// MULTI 户 Σqty 指纹（101524.64）禁入映射，各币种池按分币键取原生量实拉值；
// 未持币种池映射缺=剔除（非指纹覆写非陈值保留）。
func TestDailyCut_LiveMapPerCcy(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE fin_nav_daily, fin_strategy_budget, fin_strategy,
		fin_venue_account_snapshot, fin_capability_constraint, autotrade_state, strategy_stats CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	defer func() {
		_, _ = st.Pool().Exec(ctx, `TRUNCATE fin_nav_daily, fin_strategy_budget, fin_strategy,
			fin_venue_account_snapshot, fin_capability_constraint, autotrade_state, strategy_stats CASCADE`)
	}()
	defer func() { liveEquityMapFn = nil }()
	now := time.Now()
	// 快照池：binance:earn 双币（快照行已含指纹污染前的旧真值——live 覆写后
	// 应以分币键为准）、binance:perp USDT、mt USD（免覆写）。
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_venue_account_snapshot
		(venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at) VALUES
		('binance','earn',1,1,1,'USDT',$1),('binance','earn',1,1,1,'DOGE',$1),
		('binance','perp',1300,1344,1344,'USDT',$1),('mtacct','mt5',4247,4247,4247,'USD',$1)`, now); err != nil {
		t.Fatalf("seed snapshots: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO broker_accounts (venue, venue_type, host, port, password, is_real)
		VALUES ('mtacct','mt5','h',443,'p',true) ON CONFLICT (venue) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = st.Pool().Exec(ctx, `DELETE FROM broker_accounts WHERE venue='mtacct'`) }()
	// liveMap 分币键（MULTI 户生产侧只产 Holdings 分币键——指纹值不在场）。
	liveEquityMapFn = func(ctx context.Context) (map[string]float64, error) {
		return map[string]float64{
			"binance:earn:USDT": 1592.5, "binance:earn:DOGE": 52886,
			"binance:perp:USDT": 1344.2,
		}, nil
	}
	dayStart := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	if err := DailyCut(ctx, st, now, dayStart, 1e9); err != nil {
		t.Fatal(err)
	}
	rows, err := st.LatestNavDaily(ctx)
	if err != nil || len(rows) != 3 {
		t.Fatalf("应三行: %v %v", rows, err)
	}
	got := map[string]float64{}
	for _, r := range rows {
		got[r.Currency] = r.NavUSD
	}
	// USDT 桶=1592.5+1344.2=2936.7（分币实拉和）；DOGE 桶=52886 原生量；
	// USD 桶=4247（mt 免覆写）。指纹 101524.64 任何桶不得出现。
	if got["USDT"] != 2936.7 {
		t.Fatalf("USDT 桶=%v want 2936.7", got["USDT"])
	}
	if got["DOGE"] != 52886 {
		t.Fatalf("DOGE 桶=%v want 52886", got["DOGE"])
	}
	if got["USD"] != 4247 {
		t.Fatalf("USD 桶=%v want 4247", got["USD"])
	}
	for c, v := range got {
		if v == 101524.64 {
			t.Fatalf("%s 桶被指纹注血", c)
		}
	}
}
