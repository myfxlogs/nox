// d037_close_est_income_test.go —— D-037 验收锚：平仓净估含已收息。
// ① MT 腿=帧携 Order.Swap（bp.Swap 原生透传）；② CEX 系统仓（position_id>0）=
// position_cashflows funding/swap actual 合计（commission/estimated 不计）；
// ③ CEX 外单=SumIncomeWindow(openedAt→now)；④ openedAt 缺→不含息+close_estimate_excludes_income。
package dashboard

import (
	"context"
	"testing"
	"time"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// d037Svc 真库 Service（费率 0 → est=gross+income 便于断言；CEX cs=1/MT cs=100）。
func d037Svc(t *testing.T) (*Service, *pgstore.Store) {
	t.Helper()
	ctx := context.Background()
	st := serviceTestStoreSingleInit(t) // 单例迁移库（dashboard 测试专用 DSN）
	if _, err := st.Pool().Exec(ctx,
		`TRUNCATE position_cashflows, income_events, positions, position_legs, exchange_configs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	svc := &Service{store: st}
	svc.cexContractSize = func(string, string) (float64, bool) { return 1, true }
	svc.legContractSize = func(string, string) (float64, bool) { return 100, true }
	svc.commissionFor = func(string) float64 { return 0 }
	return svc, st
}

func d037SeedPos(t *testing.T, st *pgstore.Store) int64 {
	t.Helper()
	var id int64
	if err := st.Pool().QueryRow(context.Background(),
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps)
		 VALUES ('XASSET_CARRY:XAUUSDT:a:b','XASSET_CARRY','OPEN',0) RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("seed pos: %v", err)
	}
	return id
}

func d037SeedCfg(t *testing.T, st *pgstore.Store) int64 {
	t.Helper()
	var id int64
	if err := st.Pool().QueryRow(context.Background(),
		`INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, simulated)
		 VALUES ('binance','perp','t','k','s',false)
		 ON CONFLICT (venue, venue_type, simulated) DO UPDATE SET label=EXCLUDED.label
		 RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("seed cfg: %v", err)
	}
	return id
}

// TestD037_MTSwapIncome ①：MT 腿净估=价差−费+Order.Swap 原生已收息。
func TestD037_MTSwapIncome(t *testing.T) {
	svc, _ := d037Svc(t)
	bp := &v1.BrokerPosition{
		VenueKey: "v:mt5", Symbol: "XAUUSD", Canonical: "XAUUSD", Side: "BUY",
		Volume: 1, OpenPrice: 100, CurrentPrice: 110, Fresh: true, Swap: 4.5,
	}
	est, ok := svc.closeEstimateUSD(context.Background(), bp)
	if !ok {
		t.Fatal("USD 计价全输入应得估值")
	}
	if d := est - 1004.5; d > 0.01 || d < -0.01 {
		t.Fatalf("est want 1000+4.5=1004.5, got %v", est)
	}
	if bp.CloseEstimateExcludesIncome {
		t.Fatal("MT 腿 Order.Swap 恒在场，不应打缺息标")
	}
}

// TestD037_CEXPosCashflows ②：CEX 系统仓净估+=position_cashflows funding/swap actual；
// commission 与 quality='estimated' 不计。
func TestD037_CEXPosCashflows(t *testing.T) {
	svc, st := d037Svc(t)
	ctx := context.Background()
	posID := d037SeedPos(t, st)
	for _, c := range []struct {
		kind string
		amt  float64
		q    *string
		leg  int
	}{
		{"funding", 0.5, nil, 0},
		{"swap", 0.01, strptr("actual"), 0},
		{"commission", -0.02, strptr("actual"), 0}, // 不计：费项已由 est 覆盖退出费
		{"funding", 9.0, strptr("estimated"), 1},   // 不计：非 actual（异 leg_no 避 per_leg_uniq）
		{"funding", 8.0, strptr("unknown"), 2},     // 不计：非 actual
	} {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO position_cashflows (position_id, leg_no, kind, venue, venue_type, amount_usd, ts, quality)
			 VALUES ($1,$4,$2,'binance','perp',$3,now(),$5)`, posID, c.kind, c.amt, c.leg, c.q); err != nil {
			t.Fatalf("seed cashflow %s: %v", c.kind, err)
		}
	}
	bp := &v1.BrokerPosition{
		VenueKey: "binance:perp", Symbol: "XAUUSDT", Canonical: "XAUUSDT", Side: "SELL",
		Volume: 1, OpenPrice: 110, CurrentPrice: 100, Fresh: true, PositionId: posID,
	}
	est, ok := svc.closeEstimateUSD(context.Background(), bp)
	if !ok {
		t.Fatal("应得估值")
	}
	// gross=(100-110)*1*1*-1=10；income=0.51 → est=10.51
	if d := est - 10.51; d > 0.01 || d < -0.01 {
		t.Fatalf("est want 10+0.51=10.51, got %v", est)
	}
	if bp.CloseEstimateExcludesIncome {
		t.Fatal("cashflows 查询成功不应打缺息标")
	}
}

// TestD037_CEXExternalIncomeWindow ③：CEX 外单（PositionId=0）净估+=income_events
// openedAt→now 窗内合计；窗外/异 symbol 不计。
func TestD037_CEXExternalIncomeWindow(t *testing.T) {
	svc, st := d037Svc(t)
	ctx := context.Background()
	cfgID := d037SeedCfg(t, st)
	opened := time.Now().Add(-2 * time.Hour)
	for i, r := range []struct {
		sym string
		amt float64
		ts  time.Time
	}{
		{"XAUUSDT", 0.3, opened.Add(10 * time.Minute)},
		{"XAUUSDT", 0.2, opened.Add(time.Hour)},
		{"XAUUSDT", 9.0, opened.Add(-time.Hour)},       // 窗外不计
		{"BTCUSDT", 9.0, opened.Add(30 * time.Minute)}, // 异 symbol 不计
	} {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO income_events (venue, venue_type, exchange_config_id, symbol, income_type, amount_usd, tran_id, ts)
			 VALUES ('binance','perp',$1,$2,'FUNDING_FEE',$3,$4,$5)`,
			cfgID, r.sym, r.amt, 9000+i, r.ts); err != nil {
			t.Fatalf("seed income: %v", err)
		}
	}
	bp := &v1.BrokerPosition{
		VenueKey: "binance:perp", Symbol: "XAUUSDT", Canonical: "XAUUSDT", Side: "BUY",
		Volume: 1, OpenPrice: 100, CurrentPrice: 110, Fresh: true,
		OpenedAt: timestamppb.New(opened),
	}
	est, ok := svc.closeEstimateUSD(context.Background(), bp)
	if !ok {
		t.Fatal("应得估值")
	}
	// gross=10；income=0.5 → est=10.5
	if d := est - 10.5; d > 0.01 || d < -0.01 {
		t.Fatalf("est want 10+0.5=10.5, got %v", est)
	}
	if bp.CloseEstimateExcludesIncome {
		t.Fatal("income 窗查询成功不应打缺息标")
	}
}

// TestD037_NoOpenedAtExcludes ④：CEX 外单 openedAt 缺 → 净估仍出（价差−费）
// 但不含息并打 close_estimate_excludes_income。
func TestD037_NoOpenedAtExcludes(t *testing.T) {
	svc, _ := d037Svc(t)
	bp := &v1.BrokerPosition{
		VenueKey: "binance:perp", Symbol: "XAUUSDT", Canonical: "XAUUSDT", Side: "BUY",
		Volume: 1, OpenPrice: 100, CurrentPrice: 110, Fresh: true,
	}
	est, ok := svc.closeEstimateUSD(context.Background(), bp)
	if !ok {
		t.Fatal("openedAt 缺仍应出估值（价差−费）")
	}
	if d := est - 10.0; d > 0.01 || d < -0.01 {
		t.Fatalf("est want 10.0（不含息）, got %v", est)
	}
	if !bp.CloseEstimateExcludesIncome {
		t.Fatal("openedAt 缺应打缺息标")
	}
}

func strptr(s string) *string { return &s }
