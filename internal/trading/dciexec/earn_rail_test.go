// earn_rail_test.go —— earn 执行径多 venue 化锚：rail 分派（okx/gate 提案走
// 对应 venue 注入面+账本行 venue 键）+凭据缺 credential 缺口+binance 旧注口回归。
package dciexec

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// fakeRail 测试 rail（动词调用计数+返回值注入）。
type fakeRail struct {
	prodSub  int
	freeBal  float64
	products []EarnProduct
	subErr   error
}

func (r *fakeRail) Products(_ context.Context, _ string) ([]EarnProduct, error) {
	return r.products, nil
}
func (r *fakeRail) Subscribe(_ context.Context, _ string, _ float64) (string, error) {
	r.prodSub++
	return `{"ok":true}`, r.subErr
}
func (r *fakeRail) Redeem(context.Context, string, float64) error { return nil }
func (r *fakeRail) FreeBalance(_ context.Context, _ string) (float64, error) {
	return r.freeBal, nil
}

func railFixture(venue string, rail EarnRail) (*Executor, *fakeStore) {
	f := newFake()
	f.poolRows = []pgstore.CapitalPoolRow{
		{Venue: venue, VenueType: "unified", Equity: 1956, Currency: "USDT"},
	}
	ex := &Executor{
		Store: f, Now: time.Now,
		EarnRails: map[string]EarnRail{venue: rail},
	}
	return ex, f
}

// TestEarnRail_OKX okx 提案走 okx rail：余额源/产品面/申购全注入面，账本行 venue=okx。
func TestEarnRail_OKX(t *testing.T) {
	rail := &fakeRail{freeBal: 1956, products: []EarnProduct{
		{ProductID: "okx-saving-USDT", Asset: "USDT", APR: 0.02, CanPurchase: true}}}
	ex, f := railFixture("okx", rail)
	f.nextProp++
	f.proposals[f.nextProp] = &pgstore.Proposal{
		ID: f.nextProp, TemplateID: "cash_mgmt", ExecTrack: "auto",
		Venue: "okx", Status: "proposed"}
	f.evals[f.nextProp] = &pgstore.OppEval{ProposalID: f.nextProp, Verdict: "viable"}
	msg, err := ex.GoProposal(context.Background(), f.nextProp)
	if err != nil {
		t.Fatalf("go: %v", err)
	}
	if rail.prodSub != 1 {
		t.Fatalf("okx 申购调用=%d", rail.prodSub)
	}
	var o *pgstore.DCIOrder
	for _, x := range f.orders {
		o = x
	}
	if o == nil || o.Venue != "okx" || o.OptType != "EARN" || o.Status != "filled" {
		t.Fatalf("账本行 venue/态错: %+v", o)
	}
	if !strings.Contains(msg, "1956.0000") {
		t.Errorf("msg: %s", msg)
	}
}

// TestEarnRail_GateCredMissing gate 凭据/ rail 缺席→credential 缺口行（不臆造）。
func TestEarnRail_GateCredMissing(t *testing.T) {
	f := newFake()
	ex := &Executor{Store: f, Now: time.Now}
	f.nextProp++
	f.proposals[f.nextProp] = &pgstore.Proposal{
		ID: f.nextProp, TemplateID: "cash_mgmt", ExecTrack: "auto",
		Venue: "gate", Status: "proposed"}
	f.evals[f.nextProp] = &pgstore.OppEval{ProposalID: f.nextProp, Verdict: "viable"}
	_, err := ex.GoProposal(context.Background(), f.nextProp)
	if err == nil {
		t.Fatal("gate 无凭据应如实拒")
	}
	if len(f.gaps) == 0 {
		t.Fatal("credential 缺口缺")
	}
	if g := f.gaps[len(f.gaps)-1]; g.Kind != "credential" || !strings.Contains(g.Subject, "gate") {
		t.Fatalf("credential 缺口错: %+v", g)
	}
	if len(f.orders) != 0 {
		t.Fatal("凭据缺不应落账本行")
	}
}

// TestEarnRail_BinanceLegacyFn 旧注口（EarnProductsFn/EarnSubscribeFn）兼容回归：
// EarnRails/EarnCreds 未填 binance 径照走原实现面。
func TestEarnRail_BinanceLegacyFn(t *testing.T) {
	ex, f, calls := earnFixture()
	pid := f.addProposal("cash_mgmt")
	if _, err := ex.GoProposal(context.Background(), pid); err != nil {
		t.Fatalf("binance 旧注口回归: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("申购调用=%d", *calls)
	}
}
