// d032_risk_lines_test.go —— D-032（D-030 风控硬线）锚测试：
// ① 30d 账本回撤跨线触发 kill 一次且幂等；② 事故仓未 ack 拒开 / ack 后放行；
// ③ 权益不足拒开 / 快照缺拒开 / 超龄拒开 / 在仓名义计数；④ 周报文案快照。
package autotrade

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// d032Truncate 清账本/事故域表（setupLiveGateStore 已清 positions/legs；本函数补 settlements+controls+app_config）。
func d032Truncate(t *testing.T, s *pgstore.Store) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(),
		`TRUNCATE position_settlements, execution_controls, execution_control_events, app_config`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// d032SeedClosed 落一行 CLOSED 持仓 + settlement（net_realized_usd 可控）。
func d032SeedClosed(t *testing.T, s *pgstore.Store, oppID string, net float64) {
	t.Helper()
	ctx := context.Background()
	var posID int64
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl)
		 VALUES ($1, 'SWAPARB', 'CLOSED', now()-interval '2 day', now(), $2) RETURNING id`,
		oppID, net).Scan(&posID); err != nil {
		t.Fatalf("seed closed pos: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, settled_at,
		  net_realized_usd, pnl_quality, completeness_status, stats_status, settlement_event_key)
		 VALUES ($1,'v',10,$2, now(), $2, 'actual','complete','applied', $3)`,
		posID, net, "d032:"+oppID); err != nil {
		t.Fatalf("seed settlement: %v", err)
	}
}

// ① 回撤跨线触发 kill 一次且幂等。
func TestD032_DD30_KillOnceIdempotent(t *testing.T) {
	s := setupLiveGateStore(t, "vA:mt5", "vB:mt5", false, false)
	d032Truncate(t, s)
	ctx := context.Background()
	cp := NewControlPlane(s)

	d032SeedClosed(t, s, "D032:a", -400)
	RiskDD30Check(ctx, s, cp, -300)
	if on, err := cp.IsKillSwitchActive(ctx); err != nil || !on {
		t.Fatalf("锚1: want kill active, on=%v err=%v", on, err)
	}
	// 幂等：第二次评估不重复触发（trigger 事件数恒 1）。
	RiskDD30Check(ctx, s, cp, -300)
	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM execution_control_events WHERE event_key LIKE 'killswitch-trigger-%'`).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if n != 1 {
		t.Fatalf("锚1: want 1 trigger event, got %d", n)
	}
}

// ① 未跨线不触发（-200 > -300）。
func TestD032_DD30_NoBreachNoKill(t *testing.T) {
	s := setupLiveGateStore(t, "vA:mt5", "vB:mt5", false, false)
	d032Truncate(t, s)
	cp := NewControlPlane(s)
	d032SeedClosed(t, s, "D032:b", -200)
	RiskDD30Check(context.Background(), s, cp, -300)
	if on, _ := cp.IsKillSwitchActive(context.Background()); on {
		t.Fatal("锚2: kill should stay inactive under threshold")
	}
}

// ② 事故仓未 ack 拒开 / ack 后放行（真库 Gate.Submit 全链）。
func TestD032_IncidentGate_UnackedBlocksAckedReleases(t *testing.T) {
	s := setupLiveGateStore(t, "vA:mt5", "vB:mt5", false, false)
	d032Truncate(t, s)
	ctx := context.Background()
	var posID int64
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, exit_kind)
		 VALUES ('D032:inc:vA:vB','XASSET_CARRY','CLOSED', now()-interval '1 day', now(), 'incident_close')
		 RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed incident pos: %v", err)
	}
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5, RiskIncidentAckID: 0}, nil)
	intent := func() *TradeIntent {
		return &TradeIntent{
			Strategy: "XASSET_CARRY", Canonical: "XAUUSD",
			VenueBuy: "vA:mt5", VenueSell: "vB:mt5", Qty: 0.01, DedupKey: "D032:opp:x:vA:vB",
			Execute: func(context.Context) (string, string, int64) { return "EXECUTED", "", 1 },
		}
	}
	res := g.Submit(ctx, intent())
	if res.Status != "REJECTED" || !strings.Contains(res.Reason, "incident position") {
		t.Fatalf("锚3: want REJECTED(incident), got %s (%s)", res.Status, res.Reason)
	}
	// ack：app_config 推到该仓 id（热生效读口直验）。
	if err := s.UpsertAppConfig(ctx, "ARB_RISK_INCIDENT_ACK_ID", strconv.FormatInt(posID, 10), "d032 test ack"); err != nil {
		t.Fatalf("ack: %v", err)
	}
	res = g.Submit(ctx, intent())
	if strings.Contains(res.Reason, "incident position") {
		t.Fatalf("锚3: ack 后仍被事故闸拒开: %s", res.Reason)
	}
}

// ③ 权益纪律闸桩件。
type d032Svc struct {
	engineService
	eq   map[string]float64
	at   map[string]time.Time
	down map[string]bool // ok=false 的 venue
	// D-071：MT 腿判定点直读——计次 + 回调（可在直读时把状态翻成有值/新鲜）。
	refreshCalls int
	onRefresh    func(d *d032Svc, vk string)
}

func (d *d032Svc) AccountEquityUSDAt(vk string) (float64, string, time.Time, bool) {
	if d.down[vk] {
		return 0, "", time.Time{}, false
	}
	at, ok := d.at[vk]
	if !ok {
		at = time.Now()
	}
	return d.eq[vk], "USD", at, true
}

func (d *d032Svc) RefreshAccountEquity(_ context.Context, vk string) {
	d.refreshCalls++
	if d.onRefresh != nil {
		d.onRefresh(d, vk)
	}
}

func d032Opp() pgstore.Opportunity {
	return pgstore.Opportunity{
		ID: "D032:opp", Type: "XASSET_CARRY", Canonical: "XAUUSD",
		VenueBuy: "3001157:mt5", VenueSell: "okx:perp",
	}
}

// ③ 快照缺/不足/充足三态 + 在仓名义计数。
func TestD032_EquityDisciplineGate(t *testing.T) {
	s := setupLiveGateStore(t, "vA:mt5", "vB:mt5", false, false)
	d032Truncate(t, s)
	cfg := Config{RiskCexEquityPct: 35, RiskCfdEquityPct: 25}
	newEng := func(svc engineService) *Engine {
		return &Engine{cfg: cfg, store: s, service: svc}
	}
	// (a) 快照缺 → 拒开
	e := newEng(&d032Svc{down: map[string]bool{"okx:perp": true}, eq: map[string]float64{"3001157:mt5": 1e5}})
	if !e.equityDisciplineGate(context.Background(), d032Opp(), 1000) {
		t.Fatal("锚4a: 快照缺应拒开")
	}
	// (b) 快照超龄（10min > 5min）→ 拒开
	e = newEng(&d032Svc{
		eq: map[string]float64{"okx:perp": 1e5, "3001157:mt5": 1e5},
		at: map[string]time.Time{"okx:perp": time.Now().Add(-10 * time.Minute)},
	})
	if !e.equityDisciplineGate(context.Background(), d032Opp(), 1000) {
		t.Fatal("锚4b: 快照超龄应拒开")
	}
	// (c) CEX 权益不足（100 < 35%×1000=350）→ 拒开
	e = newEng(&d032Svc{eq: map[string]float64{"okx:perp": 100, "3001157:mt5": 1e5}})
	if !e.equityDisciplineGate(context.Background(), d032Opp(), 1000) {
		t.Fatal("锚4c: 权益不足应拒开")
	}
	// (d) 权益充足 → 放行
	e = newEng(&d032Svc{eq: map[string]float64{"okx:perp": 500, "3001157:mt5": 500}})
	if e.equityDisciplineGate(context.Background(), d032Opp(), 1000) {
		t.Fatal("锚4d: 权益足应放行")
	}
	// (e) 在仓名义计数：okx:perp 已有 OPEN XASSET_CARRY 名义 $2000 → need=35%×3000=1050
	ctx := context.Background()
	var posID int64
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, notional_usd)
		 VALUES ('D032:open:x','XASSET_CARRY','OPEN', now(), 2000) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed open pos: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_legs (position_id, leg_no, phase, attempt_no, venue, venue_type, symbol, side, qty, price_expected, status)
		 VALUES ($1,0,'OPEN',1,'okx','perp','XAU-USDT-SWAP','SELL',1,2500,'FILLED')`, posID); err != nil {
		t.Fatalf("seed leg: %v", err)
	}
	e = newEng(&d032Svc{eq: map[string]float64{"okx:perp": 1049, "3001157:mt5": 1e5}})
	if !e.equityDisciplineGate(ctx, d032Opp(), 1000) {
		t.Fatal("锚4e: 在仓名义计入后权益不足应拒开")
	}
	e = newEng(&d032Svc{eq: map[string]float64{"okx:perp": 1100, "3001157:mt5": 1e5}})
	if e.equityDisciplineGate(ctx, d032Opp(), 1000) {
		t.Fatal("锚4e: 1050 线下 1100 应放行")
	}
	// (f) 非适用类型不拦（PERP_FUNDING 不在 D-030 硬线域）。
	e = newEng(&d032Svc{down: map[string]bool{"okx:perp": true}})
	opp := d032Opp()
	opp.Type = "PERP_FUNDING"
	if e.equityDisciplineGate(ctx, opp, 1000) {
		t.Fatal("锚4f: PERP_FUNDING 不应被本闸拦")
	}
}

// ④ 周报文案快照（种子账本 → 关键行断言）。
func TestD032_ReportText(t *testing.T) {
	s := setupLiveGateStore(t, "vA:mt5", "vB:mt5", false, false)
	d032Truncate(t, s)
	ctx := context.Background()
	d032SeedClosed(t, s, "D032:r1", -50)
	// 事故仓（30d 内 incident_close + settlement -100，使全样本/剔事故两列分叉）+ OPEN 仓名义 $1500。
	var incID int64
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, exit_kind)
		 VALUES ('D032:inc','XASSET_CARRY','CLOSED', now(), now(), 'incident_close') RETURNING id`).Scan(&incID); err != nil {
		t.Fatalf("seed incident pos: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, settled_at,
		  net_realized_usd, pnl_quality, completeness_status, stats_status, settlement_event_key)
		 VALUES ($1,'v',10,-100, now(), -100,'actual','complete','applied','d032:inc')`, incID); err != nil {
		t.Fatalf("seed incident settlement: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at)
		 VALUES ('D032:open','XASSET_CARRY','OPEN', now())`); err != nil {
		t.Fatalf("seed open pos: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`UPDATE positions SET notional_usd = 1500 WHERE opportunity_id='D032:open'`); err != nil {
		t.Fatalf("seed notional: %v", err)
	}
	cp := NewControlPlane(s)
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	text, err := BuildRiskReport(ctx, s, cp, 10000, now)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, want := range []string{
		"【ARB 周报】",
		"基数 $10000",
		"30d 账本净额（全样本） $-150.00 → 年化 -18.25%",
		"30d 账本净额（剔事故） $-50.00 → 年化 -6.08%",
		"在仓 1 仓｜名义 $1500",
		"事故（30d） 1｜KillSwitch 正常",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("锚5: 文案缺 %q\n---\n%s", want, text)
		}
	}
}

// ④ 调度定点：下周一 08:00 BJ。
func TestD032_NextReportMonday(t *testing.T) {
	// 2026-10-03 是周六（UTC 12:00）→ 下一报到点=2026-10-05 08:00 BJ = 10-05 00:00 UTC。
	next := NextRiskReportAt(time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC))
	if next.Weekday() != time.Monday || next.In(riskReportLoc).Hour() != 8 {
		t.Fatalf("锚6: want Monday 08:00 BJ, got %s", next)
	}
	// 周一 07:59 BJ → 当天 08:00；周一 08:01 → 下周一。
	mon0759 := time.Date(2026, 10, 5, 7, 59, 0, 0, riskReportLoc)
	if got := NextRiskReportAt(mon0759); got.Format("2006-01-02 15:04") != "2026-10-05 08:00" {
		t.Fatalf("锚6: want same-day 08:00, got %s", got)
	}
	mon0801 := time.Date(2026, 10, 5, 8, 1, 0, 0, riskReportLoc)
	if got := NextRiskReportAt(mon0801); got.Format("2006-01-02 15:04") != "2026-10-12 08:00" {
		t.Fatalf("锚6: want next-week 08:00, got %s", got)
	}
}
