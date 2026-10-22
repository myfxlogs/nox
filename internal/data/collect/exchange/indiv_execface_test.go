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
	d, _, _, _, _ := b.indivDesiredSet(context.Background(), "perp", nil)
	if len(d) != 2 || !d["xagusdt"] || !d["ethusdt"] {
		t.Fatalf("perp desired=%v want {xagusdt,ethusdt}", d)
	}
	d, _, _, _, _ = b.indivDesiredSet(context.Background(), "spot", nil)
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
			IndivBudgetFn: func(ctx context.Context) (int64, bool, error) { return cap, ok, nil },
		}}
	}
	// cap 超=截断至 cap+over=true（字母序稳定 {a}）
	d, _, over, _, _ := mk(1, true).indivDesiredSet(context.Background(), "perp", nil)
	if !over || len(d) != 1 || !d["a"] {
		t.Fatalf("cap1: desired=%v over=%v want {a} true", d, over)
	}
	// 键缺=全空+indivOff 旗（indiv 显式关）
	d, _, _, off, _ := mk(0, false).indivDesiredSet(context.Background(), "perp", nil)
	if len(d) != 0 || off == nil || !*off {
		t.Fatalf("键缺应 indiv 关：desired=%v off=%v", d, off)
	}
	// 残④：cap 负值误配=视同 0 不 panic（空集+over）
	d, _, over, _, _ = mk(-1, true).indivDesiredSet(context.Background(), "perp", nil)
	if !over || len(d) != 0 {
		t.Fatalf("cap 负应视同 0：desired=%v over=%v", d, over)
	}
	// 键在 cap 足=全放行
	d, _, over, _, _ = mk(10, true).indivDesiredSet(context.Background(), "perp", nil)
	if over || len(d) != 3 {
		t.Fatalf("cap10: desired=%v over=%v want 3 false", d, over)
	}
}

// 返修②锚：ExecFaceFn err 径=保 last-good desired 非空拆+execface_err 计数动
// （fail-safe 出生实证——先返集再返 err，断言 desired==lastGood）。
func TestIndivDesired_ExecFaceErrKeepsLastGood(t *testing.T) {
	var fail bool
	b := &BinanceMarketWS{cfg: Config{
		ExecFaceFn: func(ctx context.Context) (map[string]string, error) {
			if fail {
				return nil, context.DeadlineExceeded
			}
			return map[string]string{"xagusdt": "perp"}, nil
		},
	}}
	ctx := context.Background()
	d, _, _, _, _ := b.indivDesiredSet(ctx, "perp", nil)
	if len(d) != 1 {
		t.Fatalf("首返集应有 xagusdt: %v", d)
	}
	lastGood := d
	fail = true
	d, _, _, _, _ = b.indivDesiredSet(ctx, "perp", lastGood)
	if !d["xagusdt"] {
		t.Fatalf("err 径应保 last-good desired 非空: %v", d)
	}
}

// 返修①锚：cap 截断时监控集让额于 exec 先占（armed 腿不被观测集挤掉）。
func TestIndivDesired_MonitorYieldsExec(t *testing.T) {
	b := &BinanceMarketWS{cfg: Config{
		ExecFaceFn: func(ctx context.Context) (map[string]string, error) {
			return map[string]string{"zzzexec": "perp", "aaaexec": "perp"}, nil
		},
		MonitorFaceFn: func(ctx context.Context) (map[string]string, error) {
			return map[string]string{"bbmon": "binance"}, nil
		},
		IndivBudgetFn: func(ctx context.Context) (int64, bool, error) { return 2, true, nil },
	}}
	d, _, over, _, _ := b.indivDesiredSet(context.Background(), "perp", nil)
	if !over || len(d) != 2 || !d["zzzexec"] || !d["aaaexec"] {
		t.Fatalf("cap=2 应两 exec 占满、monitor 让额: %v over=%v", d, over)
	}
}
