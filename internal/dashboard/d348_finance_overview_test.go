// d348_finance_overview_test.go —— D-348：GetFinanceOverview 聚合 RPC（T1-T3 先红后绿）。
// 红=现无此 RPC（基线 master 21684a40 无 GetFinanceOverview，本测编译/路由不存在即红证）。
package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/reconcile"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

// financeTestStore 财务聚合测试库（五表 TRUNCATE 隔离）。
func financeTestStore(t *testing.T) *pgstore.Store {
	t.Helper()
	s := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE ledger, positions, position_cashflows, income_events, broker_accounts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("TRUNCATE finance tables: %v", err)
	}
	return s
}

// mustExec 测试种子 SQL 捷径。
func mustExec(t *testing.T, s *pgstore.Store, q string, args ...any) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("seed %q: %v", q, err)
	}
}

// seedFinanceFixtures T1 标准夹具：三数/分层/收入构成/流水数值锚。
func seedFinanceFixtures(t *testing.T, s *pgstore.Store) {
	t.Helper()
	// 台账：+2662.8 −500 +2000 → deposited=4162.8（正=入金）
	for _, e := range []float64{2662.8, -500, 2000} {
		if _, err := s.AddLedgerEntry(context.Background(), &pgstore.LedgerEntry{
			Date: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Channel: "bank", Currency: "USD", Amount: e,
		}); err != nil {
			t.Fatalf("seed ledger: %v", err)
		}
	}
	// positions CLOSED：demo −100 / NULL(legacy real) −300 / real +50 → real 桶 −250×2、demo 桶 −100×1
	mustExec(t, s, `INSERT INTO positions (opportunity_id, type, status, opened_at, environment, realized_pnl)
		VALUES ('PERP:X:okx:perp::t1', 'PERP_FUNDING', 'CLOSED', NOW(), 'demo', -100),
		       ('PERP:X:okx:perp::t2', 'PERP_FUNDING', 'CLOSED', NOW(), NULL, -300),
		       ('PERP:X:okx:perp::t3', 'PERP_FUNDING', 'CLOSED', NOW(), 'real', 50)`)
	// cashflows：NULL 实数轨 3 行（funding 2 / swap 3 / commission 1）+ estimated 预测轨 1 行（funding 10，不入 flows）
	mustExec(t, s, `INSERT INTO position_cashflows (position_id, kind, venue, venue_type, amount_usd, ts, quality)
		VALUES (1, 'funding', 'okx','perp', 2, '2026-08-29T12:00:00Z', NULL),
		       (1, 'swap', '277842155','mt5', 3, '2026-08-29T13:00:00Z', NULL),
		       (1, 'commission', '277842155','mt5', 1, '2026-08-29T14:00:00Z', NULL),
		       (1, 'funding', 'binance','perp', 10, '2026-09-19T00:00:00Z', 'estimated')`)
	// income_events（binance 结算轨）：ts 最新（9-18）→ flows 首行
	mustExec(t, s, `INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, simulated)
		VALUES ('binance','perp','t','k','s',false) ON CONFLICT DO NOTHING`)
	mustExec(t, s, `INSERT INTO income_events (venue, venue_type, exchange_config_id, symbol, income_type, amount_usd, tran_id, ts)
		VALUES ('binance', 'perp', (SELECT id FROM exchange_configs WHERE venue='binance' AND venue_type='perp' AND NOT simulated), 'BTCUSDT', 'FUNDING_FEE', 0.5, 1001, '2026-09-18T08:00:00Z')`)
}

// TestD348_FinanceOverview_Aggregate T1：三数/分层/收入构成/流水数值锚。
func TestD348_FinanceOverview_Aggregate(t *testing.T) {
	s := financeTestStore(t)
	seedFinanceFixtures(t, s)
	// 实盘 CFD 账户（777 real connected，equity 100 USD）
	tr := true
	if err := s.InsertBrokerAccount(context.Background(), &pgstore.BrokerAccount{Venue: "777", Status: "connected", IsReal: &tr}); err != nil {
		t.Fatalf("seed broker: %v", err)
	}
	m, _, _ := newAcctMonTest(t, &fakeAccountQuerier{key: "777:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "777:mt5", Venue: "777", VenueType: "mt5", AssetType: "cfd", Currency: "USD", Equity: 100,
	}})
	if got := m.Refresh(context.Background()); len(got) != 1 {
		t.Fatalf("acct snapshot: %d", len(got))
	}
	svc := &Service{store: s, acctMon: m, qbus: bus.New(), freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.GetFinanceOverview(context.Background(), connect.NewRequest(&v1.GetFinanceOverviewRequest{}))
	if err != nil {
		t.Fatalf("GetFinanceOverview: %v", err)
	}
	r := resp.Msg
	// 三数
	if r.DepositedUsd != 4162.8 {
		t.Errorf("deposited = %v, want 4162.8", r.DepositedUsd)
	}
	if r.TotalEquityUsd != 100 {
		t.Errorf("total_equity = %v, want 100（实盘 CFD 唯一）", r.TotalEquityUsd)
	}
	if r.NetPnlUsd != 100-4162.8 {
		t.Errorf("net_pnl = %v, want %v", r.NetPnlUsd, 100-4162.8)
	}
	// 分层：demo −100×1 / real −250×2（NULL→legacy real）
	if len(r.PnlByEnv) != 2 {
		t.Fatalf("pnl_by_env rows = %d, want 2: %+v", len(r.PnlByEnv), r.PnlByEnv)
	}
	for _, p := range r.PnlByEnv {
		switch p.Environment {
		case "demo":
			if p.RealizedPnl != -100 || p.Closed != 1 {
				t.Errorf("demo 层 = %v×%d, want −100×1", p.RealizedPnl, p.Closed)
			}
		case "real":
			if p.RealizedPnl != -250 || p.Closed != 2 {
				t.Errorf("real 层 = %v×%d, want −250×2（NULL 计入 real）", p.RealizedPnl, p.Closed)
			}
		default:
			t.Errorf("意外环境 %q", p.Environment)
		}
	}
	// 收入构成（cashflows 全量口径）：commission 1×1 / funding 12×2 / swap 3×1
	if len(r.IncomeByKind) != 3 {
		t.Fatalf("income_by_kind rows = %d, want 3: %+v", len(r.IncomeByKind), r.IncomeByKind)
	}
	for _, k := range r.IncomeByKind {
		switch k.Kind {
		case "commission":
			if k.TotalUsd != 1 || k.Rows != 1 {
				t.Errorf("commission = %v×%d, want 1×1", k.TotalUsd, k.Rows)
			}
		case "funding":
			if k.TotalUsd != 12 || k.Rows != 2 {
				t.Errorf("funding = %v×%d, want 12×2（全量口径含 estimated）", k.TotalUsd, k.Rows)
			}
		case "swap":
			if k.TotalUsd != 3 || k.Rows != 1 {
				t.Errorf("swap = %v×%d, want 3×1", k.TotalUsd, k.Rows)
			}
		}
	}
	// 流水：3 ledger + 3 NULL cashflows + 1 income_event = 7（estimated 不入）；首行=income_event
	if len(r.Flows) != 7 {
		t.Fatalf("flows rows = %d, want 7（estimated 排除）: %+v", len(r.Flows), r.Flows)
	}
	first := r.Flows[0]
	if first.Source != "binance" || first.Kind != "funding_fee" || first.Symbol != "BTCUSDT" || first.AmountUsd != 0.5 {
		t.Errorf("流水首行 = %+v, want binance/funding_fee/BTCUSDT/0.5（ts 倒序首行）", first)
	}
}

// TestD348_FinanceOverview_FlowsNullTrackAndNoDoubleCount T2：estimated 不入 flows；
// NULL-quality 实数轨必入（okx/MT 漏计防线）+同 venue+kind 时间轴无重叠（双计防线 canary）。
func TestD348_FinanceOverview_FlowsNullTrackAndNoDoubleCount(t *testing.T) {
	s := financeTestStore(t)
	ctx := context.Background()
	mustExec(t, s, `INSERT INTO positions (opportunity_id, type, status, opened_at)
		VALUES ('PERP:X:okx:perp::t2', 'PERP_FUNDING', 'CLOSED', NOW()) RETURNING id`)
	mustExec(t, s, `INSERT INTO position_cashflows (position_id, kind, venue, venue_type, amount_usd, ts, quality)
		VALUES (1, 'funding', 'binance','perp', 111, '2026-08-29T12:00:00Z', NULL),
		       (1, 'funding', 'binance','perp', 999, '2026-09-19T00:00:00Z', 'estimated'),
		       (1, 'funding', 'okx','perp', 222, '2026-08-30T12:00:00Z', NULL)`)
	mustExec(t, s, `INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, simulated)
		VALUES ('binance','perp','t','k','s',false) ON CONFLICT DO NOTHING`)
	mustExec(t, s, `INSERT INTO income_events (venue, venue_type, exchange_config_id, symbol, income_type, amount_usd, tran_id, ts)
		VALUES ('binance', 'perp', (SELECT id FROM exchange_configs WHERE venue='binance' AND venue_type='perp' AND NOT simulated), 'BTCUSDT', 'FUNDING_FEE', 0.5, 1001, '2026-09-18T08:00:00Z')`)
	svc := &Service{store: s, qbus: bus.New(), freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.GetFinanceOverview(ctx, connect.NewRequest(&v1.GetFinanceOverviewRequest{}))
	if err != nil {
		t.Fatalf("GetFinanceOverview: %v", err)
	}
	var hasNullBinance, hasNullOkx, hasIncome, hasEstimated bool
	for _, f := range resp.Msg.Flows {
		if f.Source == "binance:perp" && f.AmountUsd == 111 {
			hasNullBinance = true // NULL 轨必入（防漏计）
		}
		if f.Source == "okx:perp" && f.AmountUsd == 222 {
			hasNullOkx = true
		}
		if f.Source == "binance" && f.Kind == "funding_fee" {
			hasIncome = true
		}
		if f.AmountUsd == 999 {
			hasEstimated = true // estimated 预测轨混入=双计（红）
		}
	}
	if !hasNullBinance || !hasNullOkx {
		t.Errorf("NULL 实数轨未入 flows（漏计）: binance=%v okx=%v", hasNullBinance, hasNullOkx)
	}
	if !hasIncome {
		t.Error("income_events 结算轨未入 flows")
	}
	if hasEstimated {
		t.Error("estimated 预测轨混入 flows（双计）")
	}
	// 双计 canary：同 venue+kind（binance funding）两轨时间轴无重叠（任务书 T2 锚；
	// FUNDING_FEE→funding 语义映射）。违例>0 = 未来数据形态变化，须升级 dedup 键。
	var violations int
	if err := s.Pool().QueryRow(ctx, `
		SELECT COUNT(*) FROM (
			SELECT MAX(ts) AS max_null FROM position_cashflows
			WHERE venue='binance:perp' AND kind='funding' AND COALESCE(quality,'')<>'estimated'
		) a CROSS JOIN (
			SELECT MIN(ts) AS min_ie FROM income_events WHERE venue='binance'
		) b WHERE a.max_null >= b.min_ie`).Scan(&violations); err != nil {
		t.Fatalf("canary: %v", err)
	}
	if violations != 0 {
		t.Errorf("binance funding 两轨时间轴重叠 %d 处（双计风险，dedup 键须升级）", violations)
	}
}

// TestD348_FinanceOverview_EquityRealOnly T3：权益只计实盘——CFD is_real 三态（nil 未知不计）+
// stopped 不计+CEX simulated=false 才计+非 USD 跳过计数。
func TestD348_FinanceOverview_EquityRealOnly(t *testing.T) {
	s := financeTestStore(t)
	ctx := context.Background()
	tr, fl := true, false
	seed := []struct {
		venue  string
		status string
		isReal *bool
	}{
		{"777", "connected", &tr}, // 计入
		{"778", "stopped", &tr},   // stopped 不计
		{"779", "connected", nil}, // nil 未知不计
		{"780", "connected", &fl}, // demo 不计
		{"781", "connected", &tr}, // EUR 非 USD 跳过
	}
	for _, b := range seed {
		if err := s.InsertBrokerAccount(ctx, &pgstore.BrokerAccount{Venue: b.venue, Status: b.status, IsReal: b.isReal}); err != nil {
			t.Fatalf("seed broker %s: %v", b.venue, err)
		}
	}
	m, _, _ := newAcctMonTest(t, &fakeAccountQuerier{key: "x", acct: &reconcile.AccountInfo{}})
	accts := []*reconcile.AccountInfo{
		{VenueKey: "777:mt5", AssetType: "cfd", Currency: "USD", Equity: 100},
		{VenueKey: "778:mt5", AssetType: "cfd", Currency: "USD", Equity: 200},
		{VenueKey: "779:mt5", AssetType: "cfd", Currency: "USD", Equity: 400},
		{VenueKey: "780:mt5", AssetType: "cfd", Currency: "USD", Equity: 800},
		{VenueKey: "781:mt5", AssetType: "cfd", Currency: "EUR", Equity: 1600},
		{VenueKey: "binance:spot", AssetType: "cex", Currency: "USD", Equity: 3200, Simulated: true},
		{VenueKey: "okx:spot", AssetType: "cex", Currency: "USD", Equity: 6400, Simulated: false},
	}
	for _, a := range accts {
		m.AddStatic(&fakeAccountQuerier{key: a.VenueKey, acct: a})
	}
	m.Refresh(ctx)
	svc := &Service{store: s, acctMon: m, qbus: bus.New(), freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.GetFinanceOverview(ctx, connect.NewRequest(&v1.GetFinanceOverviewRequest{}))
	if err != nil {
		t.Fatalf("GetFinanceOverview: %v", err)
	}
	// 只计 777（real CFD 100）+ okx（CEX 实盘 6400）= 6500
	if got := resp.Msg.TotalEquityUsd; got != 6500 {
		t.Errorf("total_equity = %v, want 6500（real CFD+CEX 实盘；stopped/nil/demo/simulated/非USD 全排除）", got)
	}
}
