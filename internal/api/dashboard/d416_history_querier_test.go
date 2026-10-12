// d416_history_querier_test.go —— D-416：DB 账户 attach/detach 同步注册 MT4/MT5 历史查证器
// + SetOrderHistoryQueriers 镜像钩子（501 实案：venue 历史命中但查证器从未接线）。
package dashboard

import (
	"context"
	"testing"

	"arb/internal/ledger/reconcile"
	"arb/internal/trading/exec"
)

// d416HQ fake OrderHistoryQuerier：501 probe 实证的权威平仓记录形态。
type d416HQ struct {
	venue string
	hit   *reconcile.HistoryOrder
}

func (q *d416HQ) VenueKey() string { return q.venue }

func (q *d416HQ) QueryOrderHistory(_ context.Context, _ string, _ int) (*reconcile.HistoryOrder, error) {
	return q.hit, nil
}

// T1 501 实案形态：DB 账户 attach → 钩子镜像 → recon.HistoryByVenueKey 命中
// closePrice=4316.242/profit=2.61；detach 后回退无命中（fail-closed 语义不动）。
func TestD416AttachRegistersHistQuerier(t *testing.T) {
	m := &BrokerManager{running: map[string]*running{}, reg: exec.NewRegistry(), commission: map[string]float64{}}
	recon := reconcile.New(nil)
	m.SetHistQuerierHook(func(qs []reconcile.OrderHistoryQuerier) { recon.SetOrderHistoryQueriers(qs...) })

	hit := &reconcile.HistoryOrder{
		Ticket: "2553556210", ClosePrice: 4316.242, Profit: 2.61, Symbol: "XAUUSDm",
	}
	m.attach("3001157", &running{stop: func() {}, histQuerier: &d416HQ{venue: "3001157:mt5", hit: hit}})

	h, err := recon.HistoryByVenueKey(context.Background(), "3001157:mt5", "2553556210", 3)
	if err != nil {
		t.Fatalf("HistoryByVenueKey err=%v", err)
	}
	if h == nil || h.ClosePrice != 4316.242 || h.Profit != 2.61 {
		t.Fatalf("attach 后历史查证应命中 501 实案记录，got %+v", h)
	}
}

// T2 attach/detach 跟随：stopAdapter 后查证器从聚合移除。
func TestD416DetachRemovesHistQuerier(t *testing.T) {
	m := &BrokerManager{running: map[string]*running{}, reg: exec.NewRegistry(), commission: map[string]float64{}}
	var last []reconcile.OrderHistoryQuerier
	m.SetHistQuerierHook(func(qs []reconcile.OrderHistoryQuerier) { last = qs })

	m.attach("3001157", &running{stop: func() {}, histQuerier: &d416HQ{venue: "3001157:mt5"}})
	if len(last) != 1 || last[0].VenueKey() != "3001157:mt5" {
		t.Fatalf("attach 后 histQueriers 应含 3001157:mt5，got %v", last)
	}

	m.stopAdapter("3001157")
	if len(last) != 0 {
		t.Fatalf("detach 后 histQueriers 应移除，got %v", last)
	}
}

// T3 static+running 合并：env 静态查证器与 DB 动态查证器并存在聚合输出。
func TestD416StaticAndRunningCoexist(t *testing.T) {
	m := &BrokerManager{running: map[string]*running{}, reg: exec.NewRegistry(), commission: map[string]float64{}}
	var last []reconcile.OrderHistoryQuerier
	m.SetHistQuerierHook(func(qs []reconcile.OrderHistoryQuerier) { last = qs })

	m.AddStaticHistoryQuerier("env5", &d416HQ{venue: "env5:mt5"})
	m.attach("3001157", &running{stop: func() {}, histQuerier: &d416HQ{venue: "3001157:mt5"}})

	if len(last) != 2 {
		t.Fatalf("static+running 应并存 2 个查证器，got %v", last)
	}
	seen := map[string]bool{}
	for _, q := range last {
		seen[q.VenueKey()] = true
	}
	if !seen["env5:mt5"] || !seen["3001157:mt5"] {
		t.Fatalf("合并集应含双源，got %v", seen)
	}
}

// T3b 无 attach 账户的 running（querier=nil，如 CEX/无历史面）不污染聚合。
func TestD416NilHistQuerierSkipped(t *testing.T) {
	m := &BrokerManager{running: map[string]*running{}, reg: exec.NewRegistry(), commission: map[string]float64{}}
	var last []reconcile.OrderHistoryQuerier
	m.SetHistQuerierHook(func(qs []reconcile.OrderHistoryQuerier) { last = qs })
	m.attach("v1", &running{stop: func() {}})
	if len(last) != 0 {
		t.Fatalf("nil histQuerier 不应入聚合，got %v", last)
	}
}
