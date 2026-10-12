// d549_capitalview_test.go —— D-549 验收锚：A1 待入场安全档位病灶根修
// （快照陈旧但活账本有值→不再 missing_data flapping）；A2 改线点三态
// （live 命中/REST 校准/REST 败快照兜底带龄注记）；A3 执行链 fail-closed
// （CEX 活资金不可得→拒开，快照兜底禁用于执行面）。
package dashboard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/ledger/reconcile"
	"arb/internal/store/pgstore"
)

// d549Svc 部分构造 Service（活账本+可脚本化 querier；无 store=快照径空）。
func d549Svc(q *fakeAcctQ) (*Service, *bus.BalanceLedger) {
	s := &Service{}
	l := bus.NewBalanceLedger()
	s.SetBalanceLedger(l)
	s.liveQuerierFor = func(context.Context, string) (reconcile.AccountQuerier, string, bool) {
		if q == nil {
			return nil, "", false
		}
		return q, "binance:perp", true
	}
	return s, l
}

// A1① 病灶根修：快照 >5min（旧窗必 missing_data）但活账本有值 → 档位照判。
func TestD549_ComboTier_LiveBeatsStaleSnap(t *testing.T) {
	q := &fakeAcctQ{err: errors.New("proxy dead")} // REST 败——钉死 live 命中非校准
	s, l := d549Svc(q)
	l.Put("binance:perp", 900, 1000, "USD", "ws", time.Now())
	eq := 500.0
	snaps := []pgstore.VenueAccountLatest{
		{Venue: "binance", VenueType: "perp", EquityRaw: &eq, Currency: "USD", FetchedAt: time.Now().Add(-8 * time.Minute)}, // 陈旧快照（旧码→missing_data）
		{Venue: "3001157", VenueType: "mt5", EquityRaw: &eq, Currency: "USD", FetchedAt: time.Now().Add(-8 * time.Minute)},
	}
	c := pgstore.XAssetComboAdminRow{
		VenueBuy: "binance:perp", VenueSell: "3001157:mt5",
		NotionalUSD: f64p(100),
	}
	tier, detail, _ := s.comboTier(context.Background(), c, snaps)
	if tier == "missing_data" {
		t.Fatalf("A1: 活账本有值仍 missing_data=%s（flapping 未根修）", detail)
	}
	// 两腿各判：binance=live $1000 / mt5=snap $500；needed=100 ≤ 500×cap → ok。
	if tier != "ok" {
		t.Fatalf("A1: tier=%s want ok detail=%s", tier, detail)
	}
	if q.n.Load() != 0 {
		t.Fatalf("REST calls=%d want 0（live 命中零校准）", q.n.Load())
	}
}

// A1② 三径全空 → missing_data 如实（禁伪造绿）。
func TestD549_ComboTier_AllPathsEmpty(t *testing.T) {
	s, _ := d549Svc(nil) // 无账本条目+无 querier+无快照
	c := pgstore.XAssetComboAdminRow{
		VenueBuy: "binance:perp", VenueSell: "3001157:mt5",
		NotionalUSD: f64p(100),
	}
	tier, detail, _ := s.comboTier(context.Background(), c, nil)
	if tier != "missing_data" {
		t.Fatalf("tier=%s want missing_data detail=%s", tier, detail)
	}
}

// A1③ 快照兜底非静默：CEX live 双败→snap 行判档，detail 带龄注记。
func TestD549_ComboTier_SnapFallbackNoted(t *testing.T) {
	q := &fakeAcctQ{err: errors.New("proxy dead")}
	s, _ := d549Svc(q)
	eq := 50.0 // needed=100 > 50×cap(2)=100… → over 检出 note
	snaps := []pgstore.VenueAccountLatest{
		{Venue: "binance", VenueType: "perp", EquityRaw: &eq, Currency: "USD", FetchedAt: time.Now().Add(-2 * time.Hour)},
		{Venue: "3001157", VenueType: "mt5", EquityRaw: &eq, Currency: "USD", FetchedAt: time.Now()},
	}
	c := pgstore.XAssetComboAdminRow{
		VenueBuy: "binance:perp", VenueSell: "3001157:mt5",
		NotionalUSD: f64p(101),
	}
	tier, detail, warn := s.comboTier(context.Background(), c, snaps)
	if tier != "over" || !warn {
		t.Fatalf("tier=%s warn=%v want over detail=%s", tier, warn, detail)
	}
	if !containsAll(detail, "快照兜底") {
		t.Fatalf("快照兜底未带龄注记 detail=%s", detail)
	}
}

// A3① 执行链 fail-closed：CEX 活账本空+REST 败+快照有值 → 仍拒开
// （快照兜底仅限展示面——§5 最重要安全裁定钉死）。
func TestD549_MarginFloor_CEXFailClosed(t *testing.T) {
	q := &fakeAcctQ{err: errors.New("venue 500")}
	s, _ := d549Svc(q)
	ok, known, detail := s.marginFloorEval(context.Background(), "binance:perp", 100)
	if ok {
		t.Fatalf("A3: 活资金不可得仍放行（快照兜底漏进执行面）detail=%s", detail)
	}
	if !known {
		t.Fatalf("A3: 双败应 known=true 拒开 detail=%s", detail)
	}
	if !containsAll(detail, "fail-closed") {
		t.Fatalf("detail 未标 fail-closed: %s", detail)
	}
	if q.n.Load() != 1 {
		t.Fatalf("REST calls=%d want 1（校准尝试后败）", q.n.Load())
	}
}

// A3② 执行链 live 命中：活账本有值 → 不 fail-closed（进入隐含率判定）。
// 无 buffer/无腿 → known=false fail-open（同原语义无快照行不可判——非资金数缺）。
func TestD549_MarginFloor_CEXLivePasses(t *testing.T) {
	s, l := d549Svc(nil)
	l.Put("binance:perp", 900, 1000, "USD", "ws", time.Now())
	ok, known, detail := s.marginFloorEval(context.Background(), "binance:perp", 100)
	if detail != "buffer_multiple not collected（live 资金已得，隐含率锚缺）" &&
		!containsAll(detail, "snapshot query err") && !ok {
		t.Fatalf("live 命中却被拦 ok=%v known=%v detail=%s", ok, known, detail)
	}
	if ok && !known && detail == "" {
		t.Fatal("empty detail")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
