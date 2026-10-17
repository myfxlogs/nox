package dciexec

// rotation_test.go —— D-612/D-613 息差轮动执行径单测：
// 净值差判据三态（过阈轮动/不过阈 declined/现仓已最优 declined）+持有窗守卫+
// 赎败半腿留痕。票面验收锚=净值差三态+守卫断言。

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

func rotFixture(t *testing.T, rail *fakeRail, cfg map[string]float64) (*Executor, *fakeStore, *pgstore.Proposal) {
	t.Helper()
	f := newFake()
	f.cfgF = cfg
	ex := &Executor{Store: f, Now: time.Now, EarnRails: map[string]EarnRail{"okx": rail}}
	p := &pgstore.Proposal{ID: 77, TemplateID: "earn_rotation", Venue: "okx",
		Status: "proposed", Params: map[string]any{"ccy": "USDT"}}
	f.proposals[77] = p
	return ex, f, p
}

// 过阈：现仓 1% → 目标 8%（扣时滞≈0.14pp+费 0 →净差 6.86pp>0.5pp）→赎+申+executed。
func TestRotation_EdgePassExecutes(t *testing.T) {
	rail := &fakeRail{
		positions: []EarnPosition{{ProductID: "okx-saving-USDT", Amount: 500}},
		products: []EarnProduct{
			{ProductID: "okx-saving-USDT", Asset: "USDT", APR: 0.01, CanPurchase: true},
			{ProductID: "okx-usdt-plus", Asset: "USDT", APR: 0.08, CanPurchase: true, MinPurchase: 10},
		},
	}
	ex, f, p := rotFixture(t, rail, nil)
	msg, err := ex.goEarnRotation(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if rail.redeemN != 1 || rail.prodSub != 1 {
		t.Fatalf("want redeem=1 subscribe=1, got %d/%d", rail.redeemN, rail.prodSub)
	}
	if p.Status != "executed" {
		t.Fatalf("want proposal executed, got %s", p.Status)
	}
	if !strings.Contains(msg, "okx-usdt-plus") {
		t.Fatalf("msg missing target: %s", msg)
	}
	var filled bool
	for _, o := range f.orders {
		if o.Status == "filled" && o.ProductRef == "okx-usdt-plus" {
			filled = true
		}
	}
	if !filled {
		t.Fatal("want filled EARN order for target product")
	}
}

// 不过阈：现仓 3.5% → 目标 3.7%（净差≈0.05pp<0.5pp）→declined+零调用。
func TestRotation_EdgeFailDeclines(t *testing.T) {
	rail := &fakeRail{
		positions: []EarnPosition{{ProductID: "okx-saving-USDT", Amount: 500}},
		products: []EarnProduct{
			{ProductID: "okx-saving-USDT", Asset: "USDT", APR: 0.035, CanPurchase: true},
			{ProductID: "okx-usdt-plus", Asset: "USDT", APR: 0.037, CanPurchase: true},
		},
	}
	ex, _, p := rotFixture(t, rail, nil)
	msg, err := ex.goEarnRotation(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if rail.redeemN != 0 || rail.prodSub != 0 {
		t.Fatalf("edge<阈值应零调用，got redeem=%d sub=%d", rail.redeemN, rail.prodSub)
	}
	if p.Status != "declined" {
		t.Fatalf("want declined, got %s", p.Status)
	}
	if !strings.Contains(msg, "净值差") {
		t.Fatalf("msg should report 净值差: %s", msg)
	}
}

// 时滞吃尽：现仓 5% → 目标 5.6%，lag=72h 成本=5%×72/168≈2.14pp → 净差负→declined
// （D-613② 时滞分级档生效断言）。
func TestRotation_LagCostEatsEdge(t *testing.T) {
	rail := &fakeRail{
		positions: []EarnPosition{{ProductID: "okx-saving-USDT", Amount: 500}},
		products: []EarnProduct{
			{ProductID: "okx-saving-USDT", Asset: "USDT", APR: 0.05, CanPurchase: true},
			{ProductID: "okx-usdt-plus", Asset: "USDT", APR: 0.056, CanPurchase: true},
		},
	}
	ex, _, p := rotFixture(t, rail, map[string]float64{"rotation_lag_hours_okx": 72})
	if _, err := ex.goEarnRotation(context.Background(), p); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if rail.redeemN != 0 || p.Status != "declined" {
		t.Fatalf("lag 吃尽应 declined 零赎，got redeem=%d status=%s", rail.redeemN, p.Status)
	}
}

// 已最优档 → declined 零调用。
func TestRotation_AlreadyBestDeclines(t *testing.T) {
	rail := &fakeRail{
		positions: []EarnPosition{{ProductID: "okx-usdt-plus", Amount: 500}},
		products: []EarnProduct{
			{ProductID: "okx-saving-USDT", Asset: "USDT", APR: 0.01, CanPurchase: true},
			{ProductID: "okx-usdt-plus", Asset: "USDT", APR: 0.08, CanPurchase: true},
		},
	}
	ex, _, p := rotFixture(t, rail, nil)
	if _, err := ex.goEarnRotation(context.Background(), p); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if rail.redeemN != 0 || p.Status != "declined" {
		t.Fatalf("已最优应 declined 零赎，got redeem=%d status=%s", rail.redeemN, p.Status)
	}
}

// 持有窗内 → declined 零调用（D-612③ 防费耗抖动）。
func TestRotation_HoldWindowBlocks(t *testing.T) {
	rail := &fakeRail{
		positions: []EarnPosition{{ProductID: "okx-saving-USDT", Amount: 500}},
		products: []EarnProduct{
			{ProductID: "okx-saving-USDT", Asset: "USDT", APR: 0.01, CanPurchase: true},
			{ProductID: "okx-usdt-plus", Asset: "USDT", APR: 0.08, CanPurchase: true},
		},
	}
	ex, f, p := rotFixture(t, rail, nil)
	f.lastEarnAt, f.hasEarnAt = time.Now().Add(-time.Hour), true
	msg, err := ex.goEarnRotation(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if rail.redeemN != 0 || p.Status != "declined" {
		t.Fatalf("持有窗内应 declined 零赎，got redeem=%d status=%s", rail.redeemN, p.Status)
	}
	if !strings.Contains(msg, "持有窗") {
		t.Fatalf("msg should report 持有窗: %s", msg)
	}
}

// 赎败 → 订单 failed+坑表学习+不申购（fail-closed 不臆造重试）。
func TestRotation_RedeemFailLearns(t *testing.T) {
	rail := &fakeRail{
		redeemErr: errTest{},
		positions: []EarnPosition{{ProductID: "okx-saving-USDT", Amount: 500}},
		products: []EarnProduct{
			{ProductID: "okx-saving-USDT", Asset: "USDT", APR: 0.01, CanPurchase: true},
			{ProductID: "okx-usdt-plus", Asset: "USDT", APR: 0.08, CanPurchase: true},
		},
	}
	ex, f, p := rotFixture(t, rail, nil)
	_, err := ex.goEarnRotation(context.Background(), p)
	if err == nil {
		t.Fatal("redeem 败应回 err")
	}
	if rail.prodSub != 0 {
		t.Fatal("赎败禁申购")
	}
	var failed bool
	for _, o := range f.orders {
		if o.Status == "failed" {
			failed = true
		}
	}
	if !failed {
		t.Fatal("want failed order row")
	}
	var pit bool
	for _, r := range f.pits {
		if r.Op == "earn_redeem" {
			pit = true
		}
	}
	if !pit {
		t.Fatal("want earn_redeem pit learned")
	}
}

type errTest struct{}

func (errTest) Error() string { return "venue reject: test redeem failure" }
