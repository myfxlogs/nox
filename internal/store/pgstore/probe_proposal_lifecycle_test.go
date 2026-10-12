// d048_probe_test.go —— D-048①②：探针提案持久化/CAS 流转 SQL 锚。
package pgstore

import (
	"context"
	"testing"
	"time"
)

func d048Contract() ProbeContract {
	return ProbeContract{
		Purpose: "验证 UKOIL swap 实收", Falsify: "24h |swap| > 0.01 USD",
		VenueKey: "3001:mt5", Canonical: "UKOIL", Side: "SELL", Qty: 0.1,
		NotionalUSD: 300, ExpectedCostUSD: 1.2, AutoCloseAt: time.Now().Add(2 * time.Hour),
		Basis: "D-045 取证行",
	}
}

// TestD048_ProbeProposalLifecycle insert→get→approve→run→close 全链 + dedupe 幂等。
func TestD048_ProbeProposalLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE explorer_proposals`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	day := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	id, ok, err := s.ExplorerProbeProposalInsert(ctx, d048Contract(), day)
	if err != nil || !ok || id <= 0 {
		t.Fatalf("insert: id=%d ok=%v err=%v", id, ok, err)
	}
	// dedupe：同日同 canonical+venue 重插 → 跳过零错。
	id2, ok2, err := s.ExplorerProbeProposalInsert(ctx, d048Contract(), day)
	if err != nil || ok2 || id2 != 0 {
		t.Fatalf("dedupe: id=%d ok=%v err=%v", id2, ok2, err)
	}
	p, err := s.ExplorerProbeProposalGet(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.Status != "proposed" || p.Contract.VenueKey != "3001:mt5" ||
		p.Contract.Falsify == "" || p.Contract.Qty != 0.1 {
		t.Fatalf("row=%+v contract=%+v", p, p.Contract)
	}
	// 状态机：proposed→approved→running→closed（越序 CAS 全拒）。
	if ok, _ := s.ProbeProposalRun(ctx, id, 42); ok {
		t.Fatal("proposed→running 越序通过")
	}
	if ok, err := s.ProbeProposalApprove(ctx, id, "D-048"); err != nil || !ok {
		t.Fatalf("approve: ok=%v err=%v", ok, err)
	}
	if ok, _ := s.ProbeProposalApprove(ctx, id, "D-048"); ok {
		t.Fatal("重复 approve CAS 通过")
	}
	if ok, err := s.ProbeProposalRun(ctx, id, 42); err != nil || !ok {
		t.Fatalf("run: ok=%v err=%v", ok, err)
	}
	p, _ = s.ExplorerProbeProposalGet(ctx, id)
	if p.Status != "running" || p.PositionID == nil || *p.PositionID != 42 || p.DecisionRef != "D-048" {
		t.Fatalf("running row: %+v pos=%v", p, p.PositionID)
	}
	byPos, err := s.ExplorerProbeProposalByPosition(ctx, 42)
	if err != nil || byPos.ID != id {
		t.Fatalf("by position: %+v err=%v", byPos, err)
	}
	if err := s.ProbeProposalCloseByPosition(ctx, 42); err != nil {
		t.Fatalf("close: %v", err)
	}
	p, _ = s.ExplorerProbeProposalGet(ctx, id)
	if p.Status != "closed" {
		t.Fatalf("status=%s want closed", p.Status)
	}
	// 拒绝路径：另开一行 proposed→rejected + gate_note。
	rid, ok, err := s.ExplorerProbeProposalInsert(ctx, ProbeContract{
		Purpose: "p2", Falsify: "x > 1", VenueKey: "gate:perp", Canonical: "WTI",
		Side: "BUY", Qty: 1, NotionalUSD: 100, ExpectedCostUSD: 0.5,
		AutoCloseAt: time.Now().Add(time.Hour), Basis: "b",
	}, day)
	if err != nil || !ok {
		t.Fatalf("insert2: %v %v", ok, err)
	}
	if ok, err := s.ProbeProposalReject(ctx, rid, "太贵"); err != nil || !ok {
		t.Fatalf("reject: ok=%v err=%v", ok, err)
	}
	p, _ = s.ExplorerProbeProposalGet(ctx, rid)
	if p.Status != "rejected" || p.GateNote != "太贵" {
		t.Fatalf("rejected row: %+v note=%q", p, p.GateNote)
	}
	// OpenProbe 拒回退：approved→proposed（decision_ref 清空）。
	aid, _, _ := s.ExplorerProbeProposalInsert(ctx, ProbeContract{
		Purpose: "p3", Falsify: "x > 1", VenueKey: "okx:perp", Canonical: "BRENT",
		Side: "SELL", Qty: 1, NotionalUSD: 200, ExpectedCostUSD: 0.5,
		AutoCloseAt: time.Now().Add(time.Hour), Basis: "b",
	}, day)
	if ok, _ := s.ProbeProposalApprove(ctx, aid, "D-048"); !ok {
		t.Fatal("approve3")
	}
	if ok, err := s.ProbeProposalReset(ctx, aid); err != nil || !ok {
		t.Fatalf("reset: ok=%v err=%v", ok, err)
	}
	p, _ = s.ExplorerProbeProposalGet(ctx, aid)
	if p.Status != "proposed" || p.DecisionRef != "" {
		t.Fatalf("reset row: status=%s ref=%q", p.Status, p.DecisionRef)
	}
	// scan_kind='daily' 行不可读为探针提案（Get 限定 scan_kind='probe'）。
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO explorer_proposals (scan_date, scan_kind, canonical) VALUES ($1,'daily','XAUUSD')`, day); err != nil {
		t.Fatalf("daily insert: %v", err)
	}
	var dailyID int64
	if err := s.pool.QueryRow(ctx,
		`SELECT id FROM explorer_proposals WHERE scan_kind='daily'`).Scan(&dailyID); err != nil {
		t.Fatalf("daily id: %v", err)
	}
	if _, err := s.ExplorerProbeProposalGet(ctx, dailyID); err == nil {
		t.Fatal("daily 提案可读为探针提案")
	}
}
