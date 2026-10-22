// jit_proposal_test.go —— 提案轨 jit 归集前置锚（D-658）：
// ①USDT 提案缺资+earn 足+oppAPR>earnAPR→JitSweep 调用锚（venue:spot 键+
// shortfall=需求−可动+oppAPR=卡 net_apr）；②归集败→JitErrKind 落箱携目标桶
// +额锚；③可动足→零调用；④CALL 类 BTC 担保品腿不接 jit。
package dciexec

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nox/internal/store/pgstore"
)

// GoProposal 全径锚：dci_buy_low 缺资提案→jit sweep 触发→腿径续执（sweep 缝
// 注入后 free 转足——「归集后执行」序列锚）。
func TestJitProposal_GoProposalSweepThenExec(t *testing.T) {
	fs := newFake()
	pid := fs.addProposal("dci_buy_low")
	fs.evals[pid].NetAPR = f64(0.147)
	var swept float64
	var sweptKey string
	var sweptAPR float64
	freeNow := 0.0
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		if strings.Contains(r.URL.Path, "subscribe") {
			posts++
		}
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), nil)
	e.EarnFn = func(context.Context, string) ([]EarnPosition, error) {
		return []EarnPosition{{ProductID: "p1", Amount: 1000}}, nil
	}
	e.BalanceFn = func(context.Context, string) (float64, error) { return freeNow, nil }
	e.JitSweepFn = func(_ context.Context, vk string, sf, apr float64) error {
		sweptKey, swept, sweptAPR = vk, sf, apr
		freeNow = 2000 // 归集后扣款户足（池含 earn 1000——qty 随池同涨）
		return nil
	}
	if _, err := e.GoProposal(context.Background(), pid); err != nil {
		t.Fatalf("go 败: %v", err)
	}
	if sweptKey != "binance:spot" || swept <= 0 {
		t.Fatalf("jit sweep 未按票面发: key=%s sf=%v", sweptKey, swept)
	}
	if sweptAPR != 0.147 {
		t.Fatalf("oppAPR 未取评估卡 net_apr: %v", sweptAPR)
	}
	if posts < 1 {
		t.Fatal("归集后未续执申购")
	}
}

// 归集败→按 JitErrKind 落箱携目标桶+额锚（armed 径三件套同形）。
func TestJitProposal_SweepFailGap(t *testing.T) {
	fs := newFake()
	pid := fs.addProposal("dci_buy_low")
	fs.evals[pid].NetAPR = f64(0.147)
	e := execR3(fs, "http://x", nil, nil)
	e.EarnFn = func(context.Context, string) ([]EarnPosition, error) {
		return []EarnPosition{{ProductID: "p1", Amount: 1000}}, nil
	}
	e.BalanceFn = func(context.Context, string) (float64, error) { return 0, nil }
	e.JitSweepFn = func(context.Context, string, float64, float64) error {
		return errors.New("binance earn 桶 5.00<缺口 34.00（不足归集——落箱面）")
	}
	if _, err := e.GoProposal(context.Background(), pid); err == nil ||
		!strings.Contains(err.Error(), "jit 归集败") {
		t.Fatalf("归集败应如实拒: %v", err)
	}
	if len(fs.gaps) != 1 {
		t.Fatalf("缺资落箱行缺: %+v", fs.gaps)
	}
	g := fs.gaps[0]
	if g.Kind != "capital" || g.TargetBucket != "binance:spot" || g.TargetUSD == nil || *g.TargetUSD <= 0 {
		t.Fatalf("落箱三件套错: %+v", g)
	}
}

// 可动足→零 sweep（照走径不破）。
func TestJitProposal_FundedNoSweep(t *testing.T) {
	fs := newFake()
	pid := fs.addProposal("dci_buy_low")
	calls := 0
	e := &Executor{Store: fs, ModeGate: liveModeGate}
	e.JitSweepFn = func(context.Context, string, float64, float64) error { calls++; return nil }
	e.BalanceFn = func(context.Context, string) (float64, error) { return 1e6, nil }
	e.EarnFn = func(context.Context, string) ([]EarnPosition, error) { return nil, nil }
	card, _ := fs.GetOppEval(context.Background(), pid)
	if err := e.jitProposalSweep(context.Background(),
		&pgstore.Proposal{ID: pid, TemplateID: "dci_buy_low", Venue: "binance"}, card, 0.34); err != nil {
		t.Fatalf("足资应直通: %v", err)
	}
	if calls != 0 {
		t.Fatalf("足资仍发 sweep %d 次", calls)
	}
}

// CALL 类 BTC 担保品腿不接 jit（票面③——换币=交易决策非转拨，具名另案）。
func TestJitProposal_BtcLegSkipped(t *testing.T) {
	fs := newFake()
	pid := fs.addProposal("dci_sell_high_covered_call")
	calls := 0
	e := &Executor{Store: fs, ModeGate: liveModeGate}
	e.JitSweepFn = func(context.Context, string, float64, float64) error { calls++; return nil }
	card, _ := fs.GetOppEval(context.Background(), pid)
	if err := e.jitProposalSweep(context.Background(),
		&pgstore.Proposal{ID: pid, TemplateID: "dci_sell_high_covered_call", Venue: "binance"}, card, 0.34); err != nil {
		t.Fatalf("BTC 腿应直通: %v", err)
	}
	if calls != 0 {
		t.Fatalf("BTC 担保品腿误发 sweep %d 次", calls)
	}
}
