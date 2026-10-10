// d163_probe_auto_test.go —— D-163：探针机审锚测。
// 化石 premise→拒 / 全绿→AUTO 批准开仓 / 未知前缀+歧义幂等 / 契约违约 /
// cap 违约 / 腿不可执行（非 TRADING、无鲜价、腿 facts 陈旧）/ 主闸关闭不审。
package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/config"
	"arb/internal/fact"
	"arb/internal/store/pgstore"
	"strings"

	v1 "arb/internal/gen/dashboardv1"
)

// d163Svc 在 d047Svc 基建上补机审配置（cap 键 + qbus CAPUSDT 鲜帧）。
func d163Svc(t *testing.T) (*Service, *pgstore.Store) {
	t.Helper()
	svc, st, _ := d047Svc(t)
	svc.acv.running = &config.Config{Environment: "live", ProbeMaxNotionalUSD: 1000,
		ProbeRPCEnabled: true, ProbeMaxCostUSD: 100, ProbeMaxOpen: 2}
	svc.qbus.Publish(bus.Quote{Symbol: "CAPUSDT", Venue: "tbinance", VenueType: "perp",
		VenueSymbol: "CAP-USDT-SWAP", Bid: 1.0, Ask: 1.001, Time: time.Now()})
	ctx := context.Background()
	for _, q := range []string{
		`TRUNCATE explorer_proposals`,
		`DELETE FROM facts WHERE symbol IN ('CAPUSDT','BTCUSDT')`,
		`DELETE FROM fin_cex_universe WHERE venue='tbinance'`,
		`INSERT INTO fin_cex_universe (venue, symbol, base, status)
		 VALUES ('tbinance','CAP-USDT-SWAP','CAP','TRADING')`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("d163 clean/seed: %v", err)
		}
	}
	return svc, st
}

func d163Funding(t *testing.T, st *pgstore.Store, venue string, value float64, ts time.Time) {
	t.Helper()
	if err := st.InsertFacts(context.Background(), []fact.Fact{
		{Kind: "funding", Venue: venue, Symbol: "CAPUSDT", Value: value, Ts: ts},
		{Kind: "funding_interval", Venue: venue, Symbol: "CAPUSDT", Value: 14400, Unit: "seconds", Ts: ts},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
}

func d163Proposal(t *testing.T, st *pgstore.Store, basis string) int64 {
	t.Helper()
	id, ok, err := st.ExplorerProbeProposalInsert(context.Background(), pgstore.ProbeContract{
		Purpose: "p", Falsify: "1>0", VenueKey: "tbinance:perp", Canonical: "CAPUSDT",
		Side: "BUY", Qty: 1, NotionalUSD: 100, ExpectedCostUSD: 5,
		AutoCloseAt: time.Now().Add(2 * time.Hour), Basis: basis,
	}, time.Now())
	if err != nil || !ok || id <= 0 {
		t.Fatalf("insert proposal: id=%d ok=%v err=%v", id, ok, err)
	}
	return id
}

func d163State(t *testing.T, st *pgstore.Store, id int64) (status, note string) {
	t.Helper()
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT status, COALESCE(gate_note,'') FROM explorer_proposals WHERE id=$1`, id).
		Scan(&status, &note); err != nil {
		t.Fatalf("state: %v", err)
	}
	return status, note
}

// 化石 premise：gate funding facts 停更 13 天（D-159 判例同型）→ 硬伤拒，零下单。
func TestD163_FossilPremise_Rejected(t *testing.T) {
	svc, st := d163Svc(t)
	now := time.Now()
	d163Funding(t, st, "tbinance", 10, now)
	d163Funding(t, st, "okx", 137, now)
	d163Funding(t, st, "gate", 55.407, now.Add(-13*24*time.Hour))
	pid := d163Proposal(t, st, "term_dispersion: CAPUSDT funding min=55.407 max=127.097 delta=+58.0736")
	calls := 0
	svc.probeOpener = func(context.Context, *v1.OpenProbeRequest) (*v1.OpenProbeResponse, error) {
		calls++
		return &v1.OpenProbeResponse{Status: "OPEN", PositionId: 1}, nil
	}
	svc.adjudicateProbes(context.Background())
	status, note := d163State(t, st, pid)
	if status != "rejected" || calls != 0 {
		t.Fatalf("status=%s calls=%d", status, calls)
	}
	if !strings.Contains(note, "D-163") || !strings.Contains(note, "陈旧") {
		t.Fatalf("gate_note=%q", note)
	}
}

// 全绿：鲜 facts 重算 + 可执行 + cap 内 → AUTO 批准开仓，decision_ref=D-163。
func TestD163_Green_AutoApproved(t *testing.T) {
	svc, st := d163Svc(t)
	now := time.Now()
	d163Funding(t, st, "tbinance", -5, now)
	d163Funding(t, st, "okx", 137, now)
	pid := d163Proposal(t, st, "term_dispersion CAPUSDT funding min=-5 max=137")
	var ref string
	svc.probeOpener = func(_ context.Context, r *v1.OpenProbeRequest) (*v1.OpenProbeResponse, error) {
		ref = r.GetDecisionRef()
		return &v1.OpenProbeResponse{Status: "OPEN", PositionId: 42}, nil
	}
	svc.adjudicateProbes(context.Background())
	status, note := d163State(t, st, pid)
	if status != "running" || ref != "D-163" {
		t.Fatalf("status=%s ref=%s", status, ref)
	}
	if !strings.Contains(note, "机审通过") || !strings.Contains(note, "min=") {
		t.Fatalf("gate_note=%q", note)
	}
}

// 未知 premise 前缀 → 留 proposed + gate_note + warn；重扫同判据不重复 warn（幂等）。
func TestD163_UnknownBasis_StaysProposed(t *testing.T) {
	svc, st := d163Svc(t)
	d163Funding(t, st, "tbinance", 1, time.Now())
	pid := d163Proposal(t, st, "mystery_signal: CAPUSDT x=1")
	svc.adjudicateProbes(context.Background())
	status, note := d163State(t, st, pid)
	if status != "proposed" || !strings.Contains(note, "歧义") {
		t.Fatalf("status=%s note=%q", status, note)
	}
	var warns int
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT COUNT(*) FROM alerts WHERE level='warn' AND message LIKE '%待人工裁定%'`).Scan(&warns); err != nil {
		t.Fatal(err)
	}
	if warns != 1 {
		t.Fatalf("warns=%d", warns)
	}
	svc.adjudicateProbes(context.Background()) // 再扫：gate_note 幂等键命中 → 不重复 warn
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT COUNT(*) FROM alerts WHERE level='warn' AND message LIKE '%待人工裁定%'`).Scan(&warns); err != nil {
		t.Fatal(err)
	}
	if warns != 1 {
		t.Fatalf("re-sweep warns=%d（应幂等）", warns)
	}
}

// 契约违约（auto_close_at 已过）→ 拒，零下单。
func TestD163_ContractViolation_Rejected(t *testing.T) {
	svc, st := d163Svc(t)
	d163Funding(t, st, "tbinance", 1, time.Now())
	d163Funding(t, st, "okx", 2, time.Now())
	id, ok, err := st.ExplorerProbeProposalInsert(context.Background(), pgstore.ProbeContract{
		Purpose: "p", Falsify: "1>0", VenueKey: "tbinance:perp", Canonical: "CAPUSDT",
		Side: "BUY", Qty: 1, NotionalUSD: 100, ExpectedCostUSD: 5,
		AutoCloseAt: time.Now().Add(-time.Hour), Basis: "term_dispersion CAPUSDT funding min=1 max=2",
	}, time.Now())
	if err != nil || !ok {
		t.Fatalf("insert: ok=%v err=%v", ok, err)
	}
	calls := 0
	svc.probeOpener = func(context.Context, *v1.OpenProbeRequest) (*v1.OpenProbeResponse, error) {
		calls++
		return &v1.OpenProbeResponse{Status: "OPEN"}, nil
	}
	svc.adjudicateProbes(context.Background())
	status, note := d163State(t, st, id)
	if status != "rejected" || calls != 0 || !strings.Contains(note, "auto_close_at") {
		t.Fatalf("status=%s calls=%d note=%q", status, calls, note)
	}
}

// cap：expected_cost 超 ARB_PROBE_MAX_COST → 拒；在途 ≥ARB_PROBE_MAX_OPEN → 拒。
func TestD163_CapViolation_Rejected(t *testing.T) {
	svc, st := d163Svc(t)
	now := time.Now()
	d163Funding(t, st, "tbinance", 1, now)
	d163Funding(t, st, "okx", 2, now)
	ctx := context.Background()
	// 成本超限。
	id, ok, err := st.ExplorerProbeProposalInsert(ctx, pgstore.ProbeContract{
		Purpose: "p", Falsify: "1>0", VenueKey: "tbinance:perp", Canonical: "CAPUSDT",
		Side: "BUY", Qty: 1, NotionalUSD: 100, ExpectedCostUSD: 500,
		AutoCloseAt: now.Add(time.Hour), Basis: "term_dispersion CAPUSDT funding min=1 max=2",
	}, now)
	if err != nil || !ok {
		t.Fatalf("insert: %v", err)
	}
	svc.adjudicateProbes(ctx)
	if s, note := d163State(t, st, id); s != "rejected" || !strings.Contains(note, "expected_cost") {
		t.Fatalf("cost cap: status=%s note=%q", s, note)
	}
	// 在途数超限：造 2 个 OPEN PROBE 仓。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status) VALUES (0,'PROBE','OPEN'),(0,'PROBE','OPEN')`); err != nil {
		t.Fatalf("seed probes: %v", err)
	}
	id2, ok2, err2 := st.ExplorerProbeProposalInsert(ctx, pgstore.ProbeContract{
		Purpose: "p", Falsify: "1>0", VenueKey: "tbinance:perp", Canonical: "ETHUSDT",
		Side: "BUY", Qty: 1, NotionalUSD: 100, ExpectedCostUSD: 5,
		AutoCloseAt: now.Add(time.Hour), Basis: "term_dispersion ETHUSDT funding min=1 max=2",
	}, now)
	if err2 != nil || !ok2 {
		t.Fatalf("insert2: ok=%v err=%v", ok2, err2)
	}
	pid2 := id2
	svc.adjudicateProbes(ctx)
	if s, note := d163State(t, st, pid2); s != "rejected" || !strings.Contains(note, "在途") {
		t.Fatalf("open cap: status=%s note=%q", s, note)
	}
}

// 腿不可执行：universe 非 TRADING → 拒。
func TestD163_LegNotTrading_Rejected(t *testing.T) {
	svc, st := d163Svc(t)
	now := time.Now()
	d163Funding(t, st, "tbinance", 1, now)
	d163Funding(t, st, "okx", 2, now)
	if _, err := st.Pool().Exec(context.Background(),
		`UPDATE fin_cex_universe SET status='SUSPENDED' WHERE venue='tbinance'`); err != nil {
		t.Fatal(err)
	}
	pid := d163Proposal(t, st, "term_dispersion CAPUSDT funding min=1 max=2")
	svc.adjudicateProbes(context.Background())
	if s, note := d163State(t, st, pid); s != "rejected" || !strings.Contains(note, "TRADING") {
		t.Fatalf("status=%s note=%q", s, note)
	}
}

// 主闸 ARB_PROBE_RPC_ENABLED=false → 整机审不跑（提案留 proposed，零 warn 零动作）。
func TestD163_MasterGateOff_NoAdjudication(t *testing.T) {
	svc, st := d163Svc(t)
	svc.acv.running.ProbeRPCEnabled = false
	pid := d163Proposal(t, st, "term_dispersion CAPUSDT funding min=1 max=2")
	svc.adjudicateProbes(context.Background())
	if s, note := d163State(t, st, pid); s != "proposed" || note != "" {
		t.Fatalf("status=%s note=%q", s, note)
	}
}
