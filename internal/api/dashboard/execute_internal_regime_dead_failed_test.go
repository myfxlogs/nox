package dashboard

// D-328 S4 集成锚：carry 入口 funding regime 持久性闸（manual/intent.Execute 兜底路径）。
// T1 485 回放=FAILED 带「资金费率状态未持续」；T1b 非收入腿豁免 / T2 放行 / T5 SWAPARB
// 不触发=到达后续环节（fake registry Execute 报错但 msg 不含 regime）。

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// d328SeedManualFixture 建 RUNNING state + XASSET_CARRY 机会行 + qbus 双腿报价 + allowlist/regime 注入。
// 返回就绪 Service 与 oppID。facts 由用例自种（死窗/豁免/健康）。
func d328SeedManualFixture(t *testing.T, st *pgstore.Store) (*Service, string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE autotrade_state, facts, opportunities CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("seed RUNNING: %v", err)
	}
	oppID := "XASSET_CARRY:CLUSDT:gate:perp:3001157:mt5"
	if _, err := st.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: oppID, Type: "XASSET_CARRY", Canonical: "CLUSDT",
		VenueBuy: "gate:perp", VenueSell: "3001157:mt5", NetBps: 50,
		ExpiresAt: time.Now().Add(time.Hour), QuoteTime: time.Now(),
	}); err != nil {
		t.Fatalf("seed opp: %v", err)
	}
	qb := bus.New()
	now := time.Now()
	qb.Publish(bus.Quote{Symbol: "CLUSDT", Venue: "gate", VenueType: "perp", Bid: 60, Ask: 60.02, Time: now, VenueSymbol: "CL_USDT"})
	qb.Publish(bus.Quote{Symbol: "CLUSDT", Venue: "3001157", VenueType: "mt5", Bid: 60.01, Ask: 60.03, Time: now, VenueSymbol: "USOILm"})
	svc := &Service{
		store: st, qbus: qb,
		pipeline:  exec.NewPipeline(exec.NewRegistry(), st, qb, exec.RiskLimits{}, "demo"),
		liveTrade: true, // 跳过 liveGate（聚焦本闸）
	}
	svc.SetLegContractSize(func(venueKey, venueSymbol string) (float64, bool) { return 1.0, true })
	svc.SetXAssetComboAllow([]exec.XAssetComboAllowSpec{
		{CanonicalBuy: "CLUSDT", CanonicalSell: "CLUSDT", VenueBuy: "gate:perp", VenueSell: "3001157:mt5", MaxLongQty: 1, MaxShortQty: 1000, MaxGrossUSD: 50000},
	})
	svc.SetCexContractSize(func(venue, venueSymbol string) (float64, bool) { return 1000.0, true }) // :perp 腿 CS（D-311 S3 fail-closed 需接线）
	svc.SetCarryRegime(exec.CarryRegimeConfig{Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12})
	return svc, oppID
}

// d328SeedManualFunding 种 funding 预测流样本（同 autotrade 侧 helper，store 为 SERVICE_DSN 库）。
func d328SeedManualFunding(t *testing.T, st *pgstore.Store, vals ...float64) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().Add(-time.Duration(len(vals)) * 5 * time.Minute)
	for i, v := range vals {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
			 VALUES ('funding', 'gate', 'CLUSDT', $1, 'pct_annualized', $2, 'd328test')`,
			v, base.Add(time.Duration(i)*5*time.Minute)); err != nil {
			t.Fatalf("seed fact %d: %v", i, err)
		}
	}
}

// TestD328_ExecuteInternal_RegimeDead_FAILED T1 485 回放（manual 路径）：
// 死 regime → FAILED "资金费率状态未持续"（对未接线码先红=到达 Execute 的其他报错）。
func TestD328_ExecuteInternal_RegimeDead_FAILED(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	svc, oppID := d328SeedManualFixture(t, st)
	d328SeedManualFunding(t, st, -17, -4, 0, -12, 0, 0, 0, 0, 0, 0, 0, 0, -12)

	status, msg, posID := svc.ExecuteOpportunityInternal(context.Background(), oppID, "d328-t1", 1.0)
	if status != "FAILED" || posID != 0 {
		t.Fatalf("死 regime 应 FAILED posID=0, got %s/%d", status, posID)
	}
	if !strings.Contains(msg, "资金费率状态未持续") {
		t.Errorf("msg 应带 regime 拒绝原因, got %q", msg)
	}
	// reason 带窗口统计（max=0：窗口含归零样本）供 FAILED message 可诊断
	if !strings.Contains(msg, "max=0") {
		t.Errorf("msg 应带窗口统计, got %q", msg)
	}
}

// TestD328_ExecuteInternal_NonIncomeAndPass T1b+T2（manual 路径）：豁免与健康样本放行——
// 到达 fake registry Execute（报错但 msg 不含 regime）。
func TestD328_ExecuteInternal_NonIncomeAndPass(t *testing.T) {
	st := serviceTestStoreSingleInit(t)

	// T1b：latest 归零（非收入腿）→ 豁免
	svc, oppID := d328SeedManualFixture(t, st)
	d328SeedManualFunding(t, st, -17, -4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	status, msg, _ := svc.ExecuteOpportunityInternal(context.Background(), oppID, "d328-t1b", 1.0)
	if strings.Contains(msg, "资金费率状态未持续") {
		t.Errorf("T1b 非收入腿应豁免, msg=%s", msg)
	}
	if status == "EXECUTED" {
		t.Errorf("fake registry 无执行器, 不应 EXECUTED")
	}

	// T2：全程深负 → 放行
	st2 := serviceTestStoreSingleInit(t)
	svc2, oppID2 := d328SeedManualFixture(t, st2)
	d328SeedManualFunding(t, st2, -25, -20, -18, -15, -12, -11, -10, -10, -9, -9, -8.5, -8, -2)
	status2, msg2, _ := svc2.ExecuteOpportunityInternal(context.Background(), oppID2, "d328-t2", 1.0)
	if strings.Contains(msg2, "资金费率状态未持续") {
		t.Errorf("T2 全程深负应放行, msg=%s", msg2)
	}
	if status2 == "EXECUTED" {
		t.Errorf("fake registry 无执行器, 不应 EXECUTED")
	}
}

// TestD328_ExecuteInternal_NoTrigger T5（manual 路径）：PERP_FUNDING 不触发闸
// （opportunities.type 无 SWAPARB/MEANREVERT——CHECK 约束值集见 migration 0097，回归锚取 PERP_FUNDING）
// （msg 不含 regime）。
func TestD328_ExecuteInternal_NoTrigger(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE autotrade_state, facts, opportunities CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("seed RUNNING: %v", err)
	}
	oppID := "PERP_FUNDING:BTCUSDT:binance:spot:okx:perp"
	if _, err := st.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: oppID, Type: "PERP_FUNDING", Canonical: "BTCUSDT",
		VenueBuy: "binance:spot", VenueSell: "okx:perp", NetBps: 50,
		ExpiresAt: time.Now().Add(time.Hour), QuoteTime: time.Now(),
	}); err != nil {
		t.Fatalf("seed opp: %v", err)
	}
	qb := bus.New()
	now := time.Now()
	qb.Publish(bus.Quote{Symbol: "BTCUSDT", Venue: "binance", VenueType: "spot", Bid: 60000, Ask: 60010, Time: now, VenueSymbol: "BTCUSDT"})
	qb.Publish(bus.Quote{Symbol: "BTCUSDT", Venue: "okx", VenueType: "perp", Bid: 60000, Ask: 60010, Time: now, VenueSymbol: "BTC-USDT-SWAP"})
	svc := &Service{
		store: st, qbus: qb,
		pipeline:  exec.NewPipeline(exec.NewRegistry(), st, qb, exec.RiskLimits{}, "demo"),
		liveTrade: true,
	}
	svc.SetLegContractSize(func(venueKey, venueSymbol string) (float64, bool) { return 1.0, true })
	svc.SetCexContractSize(func(venue, venueSymbol string) (float64, bool) { return 0.001, true })

	status, msg, _ := svc.ExecuteOpportunityInternal(context.Background(), oppID, "d328-t5", 1.0)
	if strings.Contains(msg, "资金费率状态未持续") {
		t.Errorf("PERP_FUNDING 不应被 regime 闸拦截, msg=%s", msg)
	}
	if status == "EXECUTED" {
		t.Errorf("fake registry 无执行器, 不应 EXECUTED")
	}
}
