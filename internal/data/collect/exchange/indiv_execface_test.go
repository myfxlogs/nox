// indiv_execface_test.go —— D-663 分层报价锚：indiv desired=执行面集（非宇宙）
// +预算闸三态（cap 超截断/键缺全关/键在放行）。
package exchange

import (
	"context"
	"testing"
)

func TestIndivDesired_ExecFaceOnly(t *testing.T) {
	b := &BinanceMarketWS{cfg: Config{
		ExecFaceFn: func(ctx context.Context) (map[string]string, error) {
			return map[string]string{"xagusdt": "perp", "btcusdt": "spot", "ethusdt": "perp"}, nil
		},
	}}
	d, _, _ := b.indivDesiredSet(context.Background(), "perp")
	if len(d) != 2 || !d["xagusdt"] || !d["ethusdt"] {
		t.Fatalf("perp desired=%v want {xagusdt,ethusdt}", d)
	}
	d, _, _ = b.indivDesiredSet(context.Background(), "spot")
	if len(d) != 1 || !d["btcusdt"] {
		t.Fatalf("spot desired=%v want {btcusdt}", d)
	}
	// 宇宙符号不入（ExecFaceFn 不提供=effectiveSymbols 零消费——D-652 病灶根治）
}

func TestIndivDesired_BudgetCap(t *testing.T) {
	mk := func(cap int64, ok bool) *BinanceMarketWS {
		return &BinanceMarketWS{cfg: Config{
			ExecFaceFn: func(ctx context.Context) (map[string]string, error) {
				return map[string]string{"a": "perp", "b": "perp", "c": "perp"}, nil
			},
			IndivBudgetFn: func(ctx context.Context) (int64, bool) { return cap, ok },
		}}
	}
	// cap 超=截断至 cap+over=true（字母序稳定 {a}）
	d, _, over := mk(1, true).indivDesiredSet(context.Background(), "perp")
	if !over || len(d) != 1 || !d["a"] {
		t.Fatalf("cap1: desired=%v over=%v want {a} true", d, over)
	}
	// 键缺=全空+indivOff 旗（indiv 显式关）
	indivOffFlag = false
	d, _, _ = mk(0, false).indivDesiredSet(context.Background(), "perp")
	if len(d) != 0 || !indivOff() {
		t.Fatalf("键缺应 indiv 关：desired=%v off=%v", d, indivOff())
	}
	// 键在 cap 足=全放行
	d, _, over = mk(10, true).indivDesiredSet(context.Background(), "perp")
	if over || len(d) != 3 {
		t.Fatalf("cap10: desired=%v over=%v want 3 false", d, over)
	}
}
