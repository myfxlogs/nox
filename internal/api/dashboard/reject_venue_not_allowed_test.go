// d047_probe_test.go —— D-047：OpenProbe 守卫链 + 到期自动平仓 + ListProbes 锚。
// 全路径禁真单：executor 为注册表内 fake（同 exec.Executor 接口）。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/platform/config"
	"arb/internal/platform/testutil/storetest"
	"arb/internal/store/pgstore"
	"arb/internal/trading/exec"

	v1 "arb/internal/gen/dashboardv1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeProbeExec D-047 测试执行器（记单不发出，禁真单）。
type fakeProbeExec struct {
	key       string
	mu        sync.Mutex
	places    []exec.Order
	closes    []exec.OrderRef
	placeFill exec.Fill
	placeErr  error
	closeFill exec.Fill
	closeErr  error
}

func (f *fakeProbeExec) Key() string { return f.key }

func (f *fakeProbeExec) PlaceOrder(ctx context.Context, o exec.Order) (exec.Fill, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.places = append(f.places, o)
	return f.placeFill, f.placeErr
}

func (f *fakeProbeExec) CloseOrder(ctx context.Context, ref exec.OrderRef) (exec.Fill, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes = append(f.closes, ref)
	return f.closeFill, f.closeErr
}

// d047Svc 装配探针路径最小 service（真 test DB + fake executor + 真实报价帧）。
// 返 (svc, store, fake)；reg 已注册 fake；allow rules 含 tbinance:perp；
// execution_controls 全=PAPER enabled（闸门默认放行——硬线拒测各自翻转）。
func d047Svc(t *testing.T) (*Service, *pgstore.Store, *fakeProbeExec) {
	t.Helper()
	ctx := context.Background()
	// 共享池+一次性 Migrate（dashboard 测试约定；service 域独立库免与 pgstore 测试抢 TRUNCATE）。
	st := storetest.SharedStore(t, "ARB_TEST_PG_SERVICE_DSN")
	for _, q := range []string{
		`TRUNCATE position_cashflows, position_settlements, position_legs, positions CASCADE`,
		`TRUNCATE alerts`,
		`TRUNCATE autotrade_allow_rules`,
		`DELETE FROM execution_controls`,
		`DELETE FROM app_config WHERE key LIKE 'ARB_PROBE%'`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("clean: %v", err)
		}
	}
	// 控制面放行（PAPER+enabled）。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO execution_controls (scope_type, scope_key, mode, enabled, updated_at)
		 VALUES ('global','','PAPER',true,now())`); err != nil {
		t.Fatalf("exec ctrl: %v", err)
	}
	// venue 白名单：tbinance:perp + 3001:mt5。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated)
		 VALUES ('venue','tbinance:perp',true,false),('venue','3001:mt5',true,false)
		 ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("allow rules: %v", err)
	}
	fx := &fakeProbeExec{key: "tbinance:perp",
		placeFill: exec.Fill{OrderID: "o-1", Price: 60001, Qty: 0.005, FeeUSD: 0.3}}
	reg := exec.NewRegistry()
	reg.Register(fx)
	qbus := bus.New()
	qbus.Publish(bus.Quote{Symbol: "BTCUSDT", Venue: "tbinance", VenueType: "perp",
		VenueSymbol: "BTC-USDT-SWAP", Bid: 60000, Ask: 60002, Time: time.Now()})
	svc := &Service{
		store: st, bm: &BrokerManager{store: st, reg: reg}, qbus: qbus,
		gateChecker: &storeGateChecker{store: st},
	}
	svc.SetAppConfigView(&config.Config{Environment: "live", ProbeMaxNotionalUSD: 1000, ProbeRPCEnabled: true}, nil, nil)
	return svc, st, fx
}

func d047Req(qty float64) *connect.Request[v1.OpenProbeRequest] {
	return connect.NewRequest(&v1.OpenProbeRequest{
		VenueKey:    "tbinance:perp",
		Canonical:   "BTCUSDT",
		Side:        "BUY",
		Qty:         qty,
		Purpose:     "d047-test",
		DecisionRef: "D-047",
		AutoCloseAt: timestamppb.New(time.Now().Add(time.Hour)),
	})
}

func d047AlertCount(t *testing.T, st *pgstore.Store, level string) int {
	t.Helper()
	var n int
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT COUNT(*) FROM alerts WHERE level=$1 AND message LIKE '[probe]%'`, level).Scan(&n); err != nil {
		t.Fatalf("alerts: %v", err)
	}
	return n
}

// 守卫拒序断言：FAILED + warn 告警落库 + 零下单。
func d047ExpectReject(t *testing.T, svc *Service, st *pgstore.Store, fx *fakeProbeExec, req *connect.Request[v1.OpenProbeRequest]) string {
	t.Helper()
	resp, err := svc.OpenProbe(context.Background(), req)
	if err != nil {
		t.Fatalf("OpenProbe err: %v", err)
	}
	if resp.Msg.GetStatus() != "FAILED" {
		t.Fatalf("status=%s, want FAILED（msg=%s）", resp.Msg.GetStatus(), resp.Msg.GetMessage())
	}
	if d047AlertCount(t, st, "warn") == 0 {
		t.Error("守卫拒绝未落 alerts warn")
	}
	if len(fx.places) != 0 {
		t.Error("守卫拒绝后仍下了单")
	}
	return resp.Msg.GetMessage()
}

// TestD047_RejectVenueNotAllowed venue 不在 autotrade_allow_rules 启用集 → 拒。
func TestD047_RejectVenueNotAllowed(t *testing.T) {
	svc, st, fx := d047Svc(t)
	req := d047Req(0.005)
	req.Msg.VenueKey = "gate:perp" // 未在白名单
	msg := d047ExpectReject(t, svc, st, fx, req)
	if msg == "" {
		t.Error("empty reject message")
	}
}

// TestD047_RejectKillSwitch 控制面 kill（enabled=false）→ 拒。
func TestD047_RejectKillSwitch(t *testing.T) {
	svc, st, fx := d047Svc(t)
	if _, err := st.Pool().Exec(context.Background(),
		`UPDATE execution_controls SET enabled=false WHERE scope_type='global'`); err != nil {
		t.Fatalf("kill: %v", err)
	}
	d047ExpectReject(t, svc, st, fx, d047Req(0.005))
}

// TestD047_RejectIncident 事故仓未归档（exit_kind=incident_close id>ack）→ 拒。
func TestD047_RejectIncident(t *testing.T) {
	svc, st, fx := d047Svc(t)
	ctx := context.Background()
	pid, err := st.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: "inc:t", Type: "FUNDING_HEDGE", Status: "CLOSED"})
	if err != nil {
		t.Fatalf("inc pos: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`UPDATE positions SET closed_at=now(), realized_pnl=-1, exit_kind='incident_close' WHERE id=$1`, pid); err != nil {
		t.Fatalf("inc mark: %v", err)
	}
	d047ExpectReject(t, svc, st, fx, d047Req(0.005))
}

// TestD047_RejectEquity 权益纪律开但快照缺 → fail-closed 拒。
func TestD047_RejectEquity(t *testing.T) {
	svc, st, fx := d047Svc(t)
	svc.SetAppConfigView(&config.Config{
		Environment: "live", ProbeMaxNotionalUSD: 1000, ProbeRPCEnabled: true,
		RiskCexEquityPct: 35, // acctMon=nil → 快照缺 → 拒
	}, nil, nil)
	d047ExpectReject(t, svc, st, fx, d047Req(0.005))
}

// TestD047_RejectOverNotional 名义 > ARB_PROBE_MAX_NOTIONAL → 拒。
func TestD047_RejectOverNotional(t *testing.T) {
	svc, st, fx := d047Svc(t)
	// 0.02 × 60001 ≈ $1200 > $1000 上限。
	msg := d047ExpectReject(t, svc, st, fx, d047Req(0.02))
	if msg == "" {
		t.Error("empty reject message")
	}
}

// TestD047_RejectInFlightFull 在途 PROBE=2 已满 → 拒。
func TestD047_RejectInFlightFull(t *testing.T) {
	svc, st, fx := d047Svc(t)
	ctx := context.Background()
	future := time.Now().Add(time.Hour)
	for _, ref := range []string{"d047-f1", "d047-f2"} {
		id, err := st.CreatePosition(ctx, &pgstore.Position{
			OpportunityID: "PROBE:BTCUSDT:" + ref, Type: pgstore.PositionTypePROBE,
			Status: "OPEN", DecisionRef: &ref, AutoCloseAt: &future})
		if err != nil {
			t.Fatalf("seed probe: %v", err)
		}
		_ = id
	}
	d047ExpectReject(t, svc, st, fx, d047Req(0.005))
}

// TestD047_OpenAndList 开单成功 → OPEN + FILLED 腿 + commission cashflow；
// ListProbes 返回 decision_ref/auto_close_at。
func TestD047_OpenAndList(t *testing.T) {
	svc, st, fx := d047Svc(t)
	ctx := context.Background()
	resp, err := svc.OpenProbe(ctx, d047Req(0.005))
	if err != nil {
		t.Fatalf("OpenProbe: %v", err)
	}
	if resp.Msg.GetStatus() != "OPEN" || resp.Msg.GetPositionId() == 0 {
		t.Fatalf("status=%s pos=%d", resp.Msg.GetStatus(), resp.Msg.GetPositionId())
	}
	if len(fx.places) != 1 || fx.places[0].Symbol != "BTC-USDT-SWAP" || fx.places[0].Side != "BUY" {
		t.Fatalf("places=%+v", fx.places)
	}
	posID := resp.Msg.GetPositionId()
	var typ, status, ref string
	if err := st.Pool().QueryRow(ctx,
		`SELECT type, status, decision_ref FROM positions WHERE id=$1`, posID).Scan(&typ, &status, &ref); err != nil {
		t.Fatalf("position: %v", err)
	}
	if typ != pgstore.PositionTypePROBE || status != "OPEN" || ref != "D-047" {
		t.Fatalf("type=%s status=%s ref=%s", typ, status, ref)
	}
	// 腿 + commission cashflow（D-013 落账）。
	var legs int
	if err := st.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM position_legs WHERE position_id=$1 AND phase='OPEN' AND status='FILLED'`, posID).Scan(&legs); err != nil {
		t.Fatalf("legs: %v", err)
	}
	if legs != 1 {
		t.Fatalf("open legs=%d", legs)
	}
	var cfs int
	if err := st.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM position_cashflows WHERE position_id=$1`, posID).Scan(&cfs); err != nil {
		t.Fatalf("cf: %v", err)
	}
	if cfs == 0 {
		t.Error("open 腿 commission cashflow 未落")
	}
	// ListProbes 返回 decision_ref。
	lr, err := svc.ListProbes(ctx, connect.NewRequest(&v1.ListProbesRequest{}))
	if err != nil {
		t.Fatalf("ListProbes: %v", err)
	}
	if len(lr.Msg.GetProbes()) != 1 {
		t.Fatalf("probes=%d", len(lr.Msg.GetProbes()))
	}
	p := lr.Msg.GetProbes()[0]
	if p.GetDecisionRef() != "D-047" || p.GetAutoCloseAt() == nil || p.GetVenueKey() != "tbinance:perp" {
		t.Fatalf("probe row=%+v", p)
	}
}

// TestD047_ExpireAutoClose 到期 OPEN 探针仓 → ExpireDueProbes 平：
// CLOSED + exit_kind='probe_expired' + CLOSE 腿 + cashflows + 不在在途集。
func TestD047_ExpireAutoClose(t *testing.T) {
	svc, st, _ := d047Svc(t)
	ctx := context.Background()

	// MT 侧 fake executor（3001:mt5 已在白名单；到期平走 bm.CloseOrder→CloseOrder）。
	mt := &fakeProbeExec{key: "3001:mt5",
		closeFill: exec.Fill{OrderID: "c-1", Price: 2000.5, Qty: 0.01, FeeUSD: 0}}
	svc.bm.reg.Register(mt)
	// MT 腿净额合成需 contract_size（legContractSize fail-closed 口；map 行 FK 依赖 broker_accounts）。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password)
		 VALUES ('3001','mt5','h',443,'p') ON CONFLICT (venue) DO NOTHING;
		 INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, contract_size)
		 VALUES ('3001','XAUUSDm','XAUUSD',100) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("symbol map: %v", err)
	}

	past := time.Now().Add(-time.Minute)
	ref := "D-047-exp"
	posID, err := st.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: "PROBE:XAUUSD:" + ref, Type: pgstore.PositionTypePROBE,
		Status: "OPEN", DecisionRef: &ref, AutoCloseAt: &past})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	now := time.Now()
	openPx := 2000.0
	if err := st.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 1, Phase: "OPEN",
		Venue: "3001:mt5", Account: "3001", Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01,
		PriceActual: &openPx, OrderID: "t-9", Status: "FILLED",
		AttemptNo: 1, FilledQty: &openPx /*占位非零*/, AvgFillPrice: &openPx, SubmittedAt: &now, Ts: now,
	}); err != nil {
		t.Fatalf("leg: %v", err)
	}

	svc.ExpireDueProbes(ctx)

	if len(mt.closes) != 1 || mt.closes[0].OrderID != "t-9" {
		t.Fatalf("closes=%+v", mt.closes)
	}
	var status, exitKind string
	var rp *float64
	if err := st.Pool().QueryRow(ctx,
		`SELECT status, COALESCE(exit_kind,''), realized_pnl FROM positions WHERE id=$1`, posID).
		Scan(&status, &exitKind, &rp); err != nil {
		t.Fatalf("pos: %v", err)
	}
	if status != "CLOSED" || exitKind != pgstore.ExitKindProbeExpired {
		t.Fatalf("status=%s exit=%s", status, exitKind)
	}
	if rp == nil {
		t.Error("realized_pnl 未合成")
	}
	var legs, cfs int
	st.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM position_legs WHERE position_id=$1`, posID).Scan(&legs)
	st.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM position_cashflows WHERE position_id=$1`, posID).Scan(&cfs)
	if legs != 2 {
		t.Errorf("legs=%d, want 2（OPEN+CLOSE）", legs)
	}
	if cfs == 0 {
		t.Error("平仓后 position_cashflows 无行（D-013 账本缺）")
	}
	if n, _ := st.CountActiveProbes(ctx); n != 0 {
		t.Errorf("active=%d, want 0", n)
	}
}

// TestD047_ExpireNotDue 未到期探针仓不动。
func TestD047_ExpireNotDue(t *testing.T) {
	svc, st, _ := d047Svc(t)
	ctx := context.Background()
	fx := &fakeProbeExec{key: "3001:mt5"}
	svc.bm.reg.Register(fx)
	future := time.Now().Add(time.Hour)
	ref := "D-047-nd"
	posID, err := st.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: "PROBE:XAUUSD:" + ref, Type: pgstore.PositionTypePROBE,
		Status: "OPEN", DecisionRef: &ref, AutoCloseAt: &future})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	svc.ExpireDueProbes(ctx)
	if len(fx.closes) != 0 {
		t.Error("未到期仓被平仓")
	}
	var status string
	st.Pool().QueryRow(ctx, `SELECT status FROM positions WHERE id=$1`, posID).Scan(&status)
	if status != "OPEN" {
		t.Errorf("status=%s, want OPEN", status)
	}
}

// TestD047_RejectRPCDisabled R1②：ARB_PROBE_RPC_ENABLED=false → 第一道守卫即拒 + warn。
func TestD047_RejectRPCDisabled(t *testing.T) {
	svc, st, fx := d047Svc(t)
	svc.SetAppConfigView(&config.Config{
		Environment: "live", ProbeMaxNotionalUSD: 1000, ProbeRPCEnabled: false,
	}, nil, nil)
	msg := d047ExpectReject(t, svc, st, fx, d047Req(0.005))
	if !strings.Contains(msg, "未启用") {
		t.Errorf("msg=%q, want 含「未启用」", msg)
	}
}

// TestD047_RejectBadDecisionRef R1③：decision_ref 非 D# 形 → InvalidArgument。
func TestD047_RejectBadDecisionRef(t *testing.T) {
	svc, _, fx := d047Svc(t)
	for _, bad := range []string{"d047", "D-47", "D-x47", "D-047a", "PROBE-1"} {
		req := d047Req(0.005)
		req.Msg.DecisionRef = bad
		_, err := svc.OpenProbe(context.Background(), req)
		if err == nil {
			t.Errorf("ref=%q 未拒", bad)
			continue
		}
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("ref=%q code=%v, want InvalidArgument", bad, connect.CodeOf(err))
		}
	}
	if len(fx.places) != 0 {
		t.Error("非法 ref 后仍下单")
	}
}

// TestD047_Unconfirmed R1⑤：下单受理未确认 → Status=UNCONFIRMED + 仓留 OPENING。
func TestD047_Unconfirmed(t *testing.T) {
	svc, st, fx := d047Svc(t)
	fx.placeErr = &exec.ErrFillUnconfirmed{OrderID: "o-x"}
	resp, err := svc.OpenProbe(context.Background(), d047Req(0.005))
	if err != nil {
		t.Fatalf("OpenProbe: %v", err)
	}
	if resp.Msg.GetStatus() != "UNCONFIRMED" {
		t.Fatalf("status=%s, want UNCONFIRMED", resp.Msg.GetStatus())
	}
	var status string
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT status FROM positions WHERE id=$1`, resp.Msg.GetPositionId()).Scan(&status); err != nil {
		t.Fatalf("pos: %v", err)
	}
	if status != "OPENING" {
		t.Errorf("pos status=%s, want OPENING（不假写 FAILED）", status)
	}
	if d047AlertCount(t, st, "critical") == 0 {
		t.Error("未确认未落 critical 告警")
	}
}

// TestD047_StuckDueAlert R1④：到期 OPENING/CLOSING 探针 → critical 30min 去重告警，状态不动。
func TestD047_StuckDueAlert(t *testing.T) {
	svc, st, _ := d047Svc(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	ref := "D-047" // R2③：合规 D# 形（^D-\d{3,}$）
	posID, err := st.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: "PROBE:XAUUSD:" + ref, Type: pgstore.PositionTypePROBE,
		Status: "OPENING", DecisionRef: &ref, AutoCloseAt: &past})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	svc.ExpireDueProbes(ctx)
	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM alerts WHERE level='critical' AND message LIKE '%探针到期仍 OPENING 待对账%'`).Scan(&n); err != nil {
		t.Fatalf("alerts: %v", err)
	}
	if n != 1 {
		t.Fatalf("stuck critical=%d, want 1", n)
	}
	var status string
	st.Pool().QueryRow(ctx, `SELECT status FROM positions WHERE id=$1`, posID).Scan(&status)
	if status != "OPENING" {
		t.Errorf("status=%s, want OPENING（不改状态）", status)
	}
	// 去重：再扫一轮不新增。
	svc.ExpireDueProbes(ctx)
	st.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM alerts WHERE level='critical' AND message LIKE '%探针到期仍 OPENING 待对账%'`).Scan(&n)
	if n != 1 {
		t.Errorf("dedup 失效 critical=%d", n)
	}
}

// TestD047_NextExpiry_OpeningFuture R2(a)：OPENING+未来到期 → 唤醒=auto_close_at。
func TestD047_NextExpiry_OpeningFuture(t *testing.T) {
	svc, st, _ := d047Svc(t)
	ctx := context.Background()
	future := time.Now().Add(2 * time.Hour)
	ref := "D-047"
	if _, err := st.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: "PROBE:XAUUSD:" + ref, Type: pgstore.PositionTypePROBE,
		Status: "OPENING", DecisionRef: &ref, AutoCloseAt: &future}); err != nil {
		t.Fatalf("probe: %v", err)
	}
	next, err := svc.NextProbeExpiryAt(ctx)
	if err != nil || next == nil {
		t.Fatalf("next=%v err=%v", next, err)
	}
	if !next.Equal(future.Truncate(time.Microsecond)) && next.Sub(future).Abs() > time.Second {
		t.Fatalf("next=%v, want auto_close_at=%v", next, future)
	}
}

// TestD047_NextExpiry_OpenExpired R2(b)：OPEN+已过期 → ≥ now+29min（不回过去、禁热循环）。
func TestD047_NextExpiry_OpenExpired(t *testing.T) {
	svc, st, _ := d047Svc(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	ref := "D-047"
	if _, err := st.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: "PROBE:XAUUSD:" + ref, Type: pgstore.PositionTypePROBE,
		Status: "OPEN", DecisionRef: &ref, AutoCloseAt: &past}); err != nil {
		t.Fatalf("probe: %v", err)
	}
	next, err := svc.NextProbeExpiryAt(ctx)
	if err != nil || next == nil {
		t.Fatalf("next=%v err=%v", next, err)
	}
	if d := time.Until(*next); d < 29*time.Minute {
		t.Fatalf("wake in %s, want ≥29min（过期仓按 30min 节奏）", d)
	}
}

// TestD047_NextExpiry_CloseFailRetry R2(c)：venue 平失败 → 仓回 OPEN+warn×1 →
// 下一次唤醒 ≥ now+29min（重试节奏非热循环）。
func TestD047_NextExpiry_CloseFailRetry(t *testing.T) {
	svc, st, _ := d047Svc(t)
	ctx := context.Background()
	mt := &fakeProbeExec{key: "3001:mt5", closeErr: fmt.Errorf("venue down")}
	svc.bm.reg.Register(mt)
	past := time.Now().Add(-time.Minute)
	ref := "D-047"
	posID, err := st.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: "PROBE:XAUUSD:" + ref, Type: pgstore.PositionTypePROBE,
		Status: "OPEN", DecisionRef: &ref, AutoCloseAt: &past})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	now := time.Now()
	px := 2000.0
	q := 0.01
	if err := st.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 1, Phase: "OPEN",
		Venue: "3001:mt5", Account: "3001", Symbol: "XAUUSDm", Side: "BUY", Qty: q,
		PriceActual: &px, OrderID: "t-f", Status: "FILLED",
		AttemptNo: 1, FilledQty: &q, AvgFillPrice: &px, SubmittedAt: &now, Ts: now,
	}); err != nil {
		t.Fatalf("leg: %v", err)
	}
	svc.ExpireDueProbes(ctx)
	if len(mt.closes) != 1 {
		t.Fatalf("closes=%d", len(mt.closes))
	}
	var status string
	st.Pool().QueryRow(ctx, `SELECT status FROM positions WHERE id=$1`, posID).Scan(&status)
	if status != "OPEN" {
		t.Fatalf("status=%s, want OPEN（平失败回退）", status)
	}
	if d047AlertCount(t, st, "warn") != 1 {
		t.Errorf("warn=%d, want 1（重试告警）", d047AlertCount(t, st, "warn"))
	}
	next, err := svc.NextProbeExpiryAt(ctx)
	if err != nil || next == nil {
		t.Fatalf("next=%v err=%v", next, err)
	}
	if d := time.Until(*next); d < 29*time.Minute {
		t.Fatalf("wake in %s, want ≥29min", d)
	}
}
