// jit_test.go —— jit 划转执行件锚测：闸三件（金额界/venue 白名单/earn 不足）+
// sweep 链（赎→二跳划转→io 幂等键记账）+return 链（净额→可动钳→回转回申）。
package dciexec

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nox/internal/store/pgstore"
)

// 金额界：shortfall<=0 全拒（fail-closed，venue 调用零发）。
func TestJitSweep_AmountBounds(t *testing.T) {
	ex, _ := railFixture("gate", &fakeRail{positions: []EarnPosition{{ProductID: "p", Amount: 1e4}}})
	for _, bad := range []float64{0, -5, 1e-12} {
		if err := ex.JitSweep(context.Background(), "gate:perp", bad, 1.0); err == nil ||
			!strings.Contains(err.Error(), "缺口额非法") {
			t.Fatalf("amount bound %.10f 未拒: %v", bad, err)
		}
	}
}

// venue 白名单：account:mt5/未知所/空键全拒（不试 rail 不碰 HTTP）。
func TestJitSweep_VenueWhitelist(t *testing.T) {
	ex, _ := railFixture("gate", &fakeRail{})
	for _, vk := range []string{"account:mt5", "bybit:perp", "gate", ""} {
		if err := ex.JitSweep(context.Background(), vk, 100, 1.0); err == nil ||
			!strings.Contains(err.Error(), "白名单外/未分型") {
			t.Fatalf("venue %q 未拒: %v", vk, err)
		}
	}
}

// earn 不足：在册<缺口→如实败不赎（落箱面前置）。
func TestJitSweep_EarnInsufficient(t *testing.T) {
	rail := &fakeRail{positions: []EarnPosition{{ProductID: "p", Amount: 50}}}
	ex, _ := railFixture("gate", rail)
	if err := ex.JitSweep(context.Background(), "gate:perp", 100, 1.0); err == nil ||
		!strings.Contains(err.Error(), "不足归集") {
		t.Fatalf("earn 不足未拒: %v", err)
	}
	if rail.redeemN != 0 {
		t.Fatal("不足仍发赎回")
	}
}

// sweep 链（gate perp 腿真 rail→httptest）：赎回缺口额（非全额）→
// POST /api/v4/wallet/transfers spot→futures→io 两跳记账携 jit-swp 幂等键。
func TestJitSweep_GateHappy(t *testing.T) {
	var calls []string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/api/v4/wallet/transfers" {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			w.Write([]byte(`{}`))
			return
		}
		if r.URL.Path == "/api/v4/earn/uni/lends" && r.Method == http.MethodGet {
			w.Write([]byte(`[{"currency":"USDT","amount":"2000"}]`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	ex, _ := railFixture("gate", &fakeRail{})
	ex.EarnRails = nil // 真 gate rail 径（EarnCreds→httptest）
	ex.EarnCreds = map[string]EarnCred{"gate": {Key: "k", Secret: "s", BaseURL: srv.URL}}
	ex.Client = srv.Client()
	fakeIOCaptures = nil

	if err := ex.JitSweep(context.Background(), "gate:perp", 998, 1.0); err != nil {
		t.Fatalf("jit sweep: %v", err)
	}
	if !strings.Contains(gotBody, `"from":"spot"`) || !strings.Contains(gotBody, `"to":"futures"`) ||
		!strings.Contains(gotBody, `"amount":"998.00000000"`) {
		t.Fatalf("transfer body 错: %s", gotBody)
	}
	var sawRedeem, sawTransfer bool
	for _, c := range calls {
		if c == "POST /api/v4/earn/uni/lends" {
			sawRedeem = true
		}
		if c == "POST /api/v4/wallet/transfers" {
			sawTransfer = true
		}
	}
	if !sawRedeem || !sawTransfer {
		t.Fatalf("sweep 链缺腿: %v", calls)
	}
	var swpKeys int
	for _, io := range fakeIOCaptures {
		if strings.HasPrefix(io.ExternalID, "jit-swp:gate:") {
			swpKeys++
		}
	}
	if swpKeys != 2 {
		t.Fatalf("io 幂等键 jit-swp 跳数 %d≠2: %+v", swpKeys, fakeIOCaptures)
	}
}

// spot 腿免二跳：binance:spot 腿赎回即达，零 transfer 发。
func TestJitSweep_SpotLegNoHop(t *testing.T) {
	rail := &fakeRail{positions: []EarnPosition{{ProductID: "p", Amount: 500}}}
	ex, _ := railFixture("binance", rail)
	fakeIOCaptures = nil
	if err := ex.JitSweep(context.Background(), "binance:spot", 100, 1.0); err != nil {
		t.Fatalf("jit sweep spot: %v", err)
	}
	if rail.redeemN != 1 {
		t.Fatalf("赎回次数 %d≠1", rail.redeemN)
	}
	var trxRows int
	for _, io := range fakeIOCaptures {
		if strings.HasSuffix(io.ExternalID, ":trx") {
			trxRows++
		}
	}
	if trxRows != 0 {
		t.Fatalf("spot 腿误记二跳 io %d 行", trxRows)
	}
}

// return 链：净额 outstanding 钳 trading 可动→回转划转→earn 回申→io 记账。
func TestJitReturn_ClampAndSubscribe(t *testing.T) {
	rail := &fakeRail{products: []EarnProduct{{ProductID: "gate-uni-USDT", CanPurchase: true}}}
	ex, st := railFixture("gate", rail)
	st.jitOutstanding = 500
	ex.TradingFreeFn = func(_ context.Context, _, _ string) (float64, error) { return 300, nil }
	ex.JitTransferFn = func(_ context.Context, _, _ string, _ float64, _, _ string) error { return nil }
	fakeIOCaptures = nil
	if err := ex.jitReturn(context.Background(), "gate", "perp"); err != nil {
		t.Fatalf("jit return: %v", err)
	}
	if rail.prodSub != 1 {
		t.Fatalf("回申 %d≠1", rail.prodSub)
	}
	var retRows int
	for _, io := range fakeIOCaptures {
		if strings.HasPrefix(io.ExternalID, "jit-ret:gate:") {
			retRows++
			if io.Amount != 300 {
				t.Fatalf("回转 io 额 %.2f≠min(500,300)", io.Amount)
			}
		}
	}
	if retRows != 2 {
		t.Fatalf("回转 io 跳数 %d≠2（trx+sub）", retRows)
	}
}

// 净额零=静默不动（无金可回）。
func TestJitReturn_ZeroOutstanding(t *testing.T) {
	rail := &fakeRail{products: []EarnProduct{{ProductID: "p", CanPurchase: true}}}
	ex, st := railFixture("gate", rail)
	st.jitOutstanding = 0
	ex.TradingFreeFn = func(_ context.Context, _, _ string) (float64, error) { return 999, nil }
	if err := ex.jitReturn(context.Background(), "gate", "perp"); err != nil {
		t.Fatalf("zero outstanding 应静默: %v", err)
	}
	if rail.prodSub != 0 {
		t.Fatal("净零仍回申")
	}
}

// JitReturnForPosition：腿 venue 集去重触达（gate×2 腿+mt5 腿→gate 只回一次）。
func TestJitReturnForPosition_LegDedup(t *testing.T) {
	rail := &fakeRail{products: []EarnProduct{{ProductID: "p", CanPurchase: true}}}
	ex, st := railFixture("gate", rail)
	st.jitOutstanding = 0 // 净零=过腿循环即返——验 venue 去重可达性（tradingFree 每所至多一调）
	st.legs = []pgstore.PositionLeg{
		{Venue: "gate:perp"}, {Venue: "gate:perp"}, {Venue: "account:mt5"},
	}
	var calls int
	ex.TradingFreeFn = func(_ context.Context, _, _ string) (float64, error) {
		calls++
		return 0, nil
	}
	ex.JitReturnForPosition(context.Background(), 42)
	if calls != 0 {
		t.Fatalf("净零仍读 tradingFree %d 次", calls)
	}
}

// TestJitSweep_HalfRevert D-? 半程残留回补锚：赎回成+二跳划转败→已赎金额
// 重申回 earn 桶（Subscribe 发+jit-swp:...:undo 记账行）+零残留告警；
// 回补败（无可购档）→warn 残留如实告。
func TestJitSweep_HalfRevert(t *testing.T) {
	// 回补成径
	rail := &fakeRail{
		positions: []EarnPosition{{ProductID: "p", Amount: 500}},
		products:  []EarnProduct{{ProductID: "gate-uni-USDT", CanPurchase: true}},
	}
	ex, st := railFixture("gate", rail)
	ex.JitTransferFn = func(_ context.Context, _, _ string, _ float64, _, _ string) error {
		return errors.New("transfer 403")
	}
	fakeIOCaptures = nil
	if err := ex.JitSweep(context.Background(), "gate:perp", 100, 1.0); err == nil ||
		!strings.Contains(err.Error(), "划转") {
		t.Fatalf("二跳败应如实返 err: %v", err)
	}
	if rail.prodSub != 1 {
		t.Fatalf("回补申购次数 %d≠1（残滞留未回补）", rail.prodSub)
	}
	var sawUndo bool
	for _, io := range fakeIOCaptures {
		if strings.HasSuffix(io.ExternalID, ":undo") {
			sawUndo = true
		}
	}
	if !sawUndo {
		t.Fatalf("io 补偿行 jit-swp:...:undo 缺: %+v", fakeIOCaptures)
	}
	for _, a := range st.alerts {
		if strings.Contains(a, "半程残留") {
			t.Fatalf("回补成不应发残留告警: %v", a)
		}
	}

	// 回补败径（无可购档）→warn 残留如实
	rail2 := &fakeRail{positions: []EarnPosition{{ProductID: "p", Amount: 500}}}
	ex2, st2 := railFixture("gate", rail2)
	ex2.JitTransferFn = func(_ context.Context, _, _ string, _ float64, _, _ string) error {
		return errors.New("transfer 403")
	}
	fakeIOCaptures = nil
	_ = ex2.JitSweep(context.Background(), "gate:perp", 100, 1.0)
	var sawWarn bool
	for _, a := range st2.alerts {
		if strings.Contains(a, "JIT 归集半程残留") {
			sawWarn = true
		}
	}
	if !sawWarn {
		t.Fatalf("回补败残留告警缺: %v", st2.alerts)
	}
}
