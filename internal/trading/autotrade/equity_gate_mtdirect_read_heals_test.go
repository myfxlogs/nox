// d071_risk_lines_test.go —— D-071 B 锚：D-032 权益闸 MT 腿缺/超龄判定点直读一次。
// ① MT 腿缺 → 直读后新鲜 → 放行且 Refresh 恰 1 次（空账户首开修复路径）；
// ② MT 腿缺 → 直读后仍缺 → fail-closed 拒开；
// ③ CEX 腿缺 → 拒开且 Refresh 0 次（CEX 有私有流账本不走直读）。
package autotrade

import (
	"context"
	"nox/internal/risk/gate"
	"testing"
	"time"
)

func d071Eng(t *testing.T, svc *d032Svc) *Engine {
	t.Helper()
	s := setupLiveGateStore(t, "vA:mt5", "vB:mt5", false, false)
	d032Truncate(t, s)
	return &Engine{cfg: gate.Config{RiskCexEquityPct: 35, RiskCfdEquityPct: 25}, store: s, service: svc}
}

// ① 空 MT 账户快照缺 → 判定点直读一次 → 新鲜权益落态 → 放行。
func TestD071_EquityGateMTDirectReadHeals(t *testing.T) {
	svc := &d032Svc{
		eq:   map[string]float64{"okx:perp": 1e5},
		down: map[string]bool{"3001157:mt5": true}, // boot 竞态/零帧：MT 腿快照缺
	}
	svc.onRefresh = func(d *d032Svc, vk string) {
		if vk == "3001157:mt5" { // 直读采到 → 落态
			delete(d.down, vk)
			d.eq[vk] = 1e5
		}
	}
	e := d071Eng(t, svc)
	if e.equityDisciplineGate(context.Background(), d032Opp(), 1000) {
		t.Fatal("MT 腿直读后权益新鲜应放行")
	}
	if svc.refreshCalls != 1 {
		t.Fatalf("判定点直读应恰一次，got %d", svc.refreshCalls)
	}
}

// ①b MT 腿快照超龄（同缺值路径）→ 直读刷新 asOf → 放行。
func TestD071_EquityGateMTDirectReadHealsStale(t *testing.T) {
	svc := &d032Svc{
		eq: map[string]float64{"okx:perp": 1e5, "3001157:mt5": 1e5},
		at: map[string]time.Time{"3001157:mt5": time.Now().Add(-10 * time.Minute)}, // 超龄
	}
	svc.onRefresh = func(d *d032Svc, vk string) {
		d.at[vk] = time.Now() // 直读采到 → asOf 刷新
	}
	e := d071Eng(t, svc)
	if e.equityDisciplineGate(context.Background(), d032Opp(), 1000) {
		t.Fatal("MT 腿超龄直读后应放行")
	}
	if svc.refreshCalls != 1 {
		t.Fatalf("直读应恰一次，got %d", svc.refreshCalls)
	}
}

// ② MT 腿直读后仍缺 → fail-closed 拒开。
func TestD071_EquityGateMTDirectReadStillMissing(t *testing.T) {
	svc := &d032Svc{
		eq:   map[string]float64{"okx:perp": 1e5},
		down: map[string]bool{"3001157:mt5": true}, // onRefresh nil=读败
	}
	e := d071Eng(t, svc)
	if !e.equityDisciplineGate(context.Background(), d032Opp(), 1000) {
		t.Fatal("直读后仍缺应 fail-closed 拒开")
	}
	if svc.refreshCalls != 1 {
		t.Fatalf("直读应恰一次，got %d", svc.refreshCalls)
	}
}

// ③ CEX 腿缺 → 拒开且零直读（CEX 有私有流账本）。
func TestD071_EquityGateCexNoDirectRead(t *testing.T) {
	svc := &d032Svc{
		eq:   map[string]float64{"3001157:mt5": 1e5},
		down: map[string]bool{"okx:perp": true},
	}
	e := d071Eng(t, svc)
	if !e.equityDisciplineGate(context.Background(), d032Opp(), 1000) {
		t.Fatal("CEX 腿缺应拒开")
	}
	if svc.refreshCalls != 0 {
		t.Fatalf("CEX 腿不走直读，refreshCalls=%d want 0", svc.refreshCalls)
	}
}
