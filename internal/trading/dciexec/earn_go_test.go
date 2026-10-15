// earn_go_test.go —— D-286 earn 提案执行径锚：闸序（凭据/敞口/可动/在售/坑表/
// 幂等）+账本行 opt_type='EARN'+态机推进。
package dciexec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// earnFixture 可执行 earn 提案环境（凭据+敞口约束+池快照+可动余额+在售品）。
func earnFixture() (*Executor, *fakeStore, *int) {
	f := newFake()
	pct := 35.0
	f.cons = append(f.cons, pgstore.CapabilityConstraint{
		Subject: "cex_exposure_pct", Qualifier: "max", ValueNum: &pct})
	f.poolRows = []pgstore.CapitalPoolRow{
		{Venue: "binance", VenueType: "spot", Equity: 1045, Currency: "USDT"},
		{Venue: "3001157", VenueType: "mt5", Equity: 4247, Currency: "USD"},
	}
	var subCalls int
	ex := &Executor{SigmaFn: testSigma,
		Store: f, BnKey: "k", BnSec: "s",
		BalanceFn: func(context.Context, string) (float64, error) { return 1045, nil },
		EarnProductsFn: func(context.Context, string) ([]EarnProduct, error) {
			return []EarnProduct{
				{ProductID: "USDT001", Asset: "USDT", APR: 0.024, CanPurchase: true, MinPurchase: 1},
				{ProductID: "USDT002", Asset: "USDT", APR: 0.036, CanPurchase: true, MinPurchase: 1},
			}, nil
		},
		EarnSubscribeFn: func(context.Context, string, float64) error { subCalls++; return nil },
		Now:             time.Now,
	}
	return ex, f, &subCalls
}

// TestEarn_HappyPath 全过闸→申购→账本行+提案 executed。
func TestEarn_HappyPath(t *testing.T) {
	ex, f, calls := earnFixture()
	pid := f.addProposal("cash_mgmt")
	msg, err := ex.GoProposal(context.Background(), pid)
	if err != nil {
		t.Fatalf("go: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("申购调用=%d", *calls)
	}
	if !strings.Contains(msg, "1045.0000") || !strings.Contains(msg, "USDT002") {
		t.Errorf("msg: %s", msg) // best APR 档 USDT002 中标
	}
	var o *pgstore.DCIOrder
	for _, x := range f.orders {
		o = x
	}
	if o == nil || o.OptType != "EARN" || o.Status != "filled" || o.ProposalID == nil || *o.ProposalID != pid {
		t.Fatalf("账本行: %+v", o)
	}
	if f.proposals[pid].Status != "executed" {
		t.Errorf("提案态: %s", f.proposals[pid].Status)
	}
}

// TestEarn_ConcentrationAlert D-288：CEX 占比敞口闸已撤——超阈仍申购放行，
// 浓度透视 info 行落账（binance 90%≥单 venue 50% 阈+CEX 总≥70% 阈同中）。
func TestEarn_ConcentrationAlert(t *testing.T) {
	ex, f, calls := earnFixture()
	f.poolRows = []pgstore.CapitalPoolRow{
		{Venue: "binance", VenueType: "spot", Equity: 9000, Currency: "USDT"},
		{Venue: "m", VenueType: "mt5", Equity: 1000, Currency: "USD"},
	}
	pid := f.addProposal("earn_apr_lending")
	if _, err := ex.GoProposal(context.Background(), pid); err != nil {
		t.Fatalf("占比超限不应再拒（D-288 撤闸）: %v", err)
	}
	if *calls != 1 {
		t.Fatal("撤闸后应放行申购")
	}
	var saw bool
	for _, a := range f.alerts {
		if strings.Contains(a, "浓度透视") && strings.Contains(a, "binance") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("浓度透视 info 行缺: %v", f.alerts)
	}
}

// TestEarn_IdleZero 可动=0→如实不执行。
func TestEarn_IdleZero(t *testing.T) {
	ex, f, calls := earnFixture()
	ex.BalanceFn = func(context.Context, string) (float64, error) { return 0, nil }
	pid := f.addProposal("cash_mgmt")
	if _, err := ex.GoProposal(context.Background(), pid); err == nil ||
		!strings.Contains(err.Error(), "可动=0") {
		t.Fatalf("可动=0 拒: %v", err)
	}
	if *calls != 0 {
		t.Error("零可动仍申购")
	}
}

// TestEarn_NoProduct 在售面零达标→拒（APR 复验）。
func TestEarn_NoProduct(t *testing.T) {
	ex, f, calls := earnFixture()
	ex.EarnProductsFn = func(context.Context, string) ([]EarnProduct, error) {
		return []EarnProduct{
			{ProductID: "X", Asset: "USDT", APR: 0, CanPurchase: true},
			{ProductID: "Y", Asset: "USDT", APR: 0.03, CanPurchase: false},
		}, nil
	}
	pid := f.addProposal("cash_mgmt")
	if _, err := ex.GoProposal(context.Background(), pid); err == nil ||
		!strings.Contains(err.Error(), "无可购档") {
		t.Fatalf("无在售拒: %v", err)
	}
	if *calls != 0 {
		t.Error("零在售仍申购")
	}
}

// TestEarn_PitSuppress 坑表 suppress 窗→零 venue 打面。
func TestEarn_PitSuppress(t *testing.T) {
	ex, f, calls := earnFixture()
	exp := time.Now().Add(time.Hour)
	f.pits = append(f.pits, pgstore.VenueReject{
		Venue: "binance", Op: "earn_subscribe", ErrorSignature: "-1:rejected",
		Action: "suppress", Hits: 2, ExpiresAt: &exp})
	pid := f.addProposal("cash_mgmt")
	if _, err := ex.GoProposal(context.Background(), pid); err == nil ||
		!strings.Contains(err.Error(), "坑表抑单") {
		t.Fatalf("坑表抑单: %v", err)
	}
	if *calls != 0 {
		t.Error("坑内仍申购")
	}
}

// TestEarn_VenueFail 申购败→账本 failed+坑表落行+runtime gap。
func TestEarn_VenueFail(t *testing.T) {
	ex, f, _ := earnFixture()
	ex.EarnSubscribeFn = func(context.Context, string, float64) error {
		return errors.New(`{"code":-2015,"msg":"Invalid API-key"}`)
	}
	pid := f.addProposal("cash_mgmt")
	if _, err := ex.GoProposal(context.Background(), pid); err == nil {
		t.Fatal("venue 败应拒")
	}
	var failed bool
	for _, o := range f.orders {
		if o.Status == "failed" {
			failed = true
		}
	}
	if !failed {
		t.Error("账本行未落 failed")
	}
	var pit bool
	for _, p := range f.pits {
		if p.Op == "earn_subscribe" {
			pit = true
		}
	}
	if !pit {
		t.Error("败因未落坑")
	}
}

// TestEarn_Idempotent 同意图在途→幂等拒零二次申购。
func TestEarn_Idempotent(t *testing.T) {
	ex, f, calls := earnFixture()
	pid := f.addProposal("cash_mgmt")
	rid := fmt.Sprintf("earn|USDT|p%d|1", pid)
	f.orders[rid] = &pgstore.DCIOrder{RequestID: rid, Status: "filled"}
	if _, err := ex.GoProposal(context.Background(), pid); err == nil ||
		!strings.Contains(err.Error(), "已受理") {
		t.Fatalf("幂等拒: %v", err)
	}
	if *calls != 0 {
		t.Error("幂等窗内仍申购")
	}
}
