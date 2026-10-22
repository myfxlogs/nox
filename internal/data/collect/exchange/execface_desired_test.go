// execface_desired_test.go —— D-665 返修①锚：err 保 last-good/cap 截断/键缺关。
package exchange

import (
	"context"
	"testing"
)

func TestExecFaceDesired_ErrKeepsLastGood(t *testing.T) {
	var fail bool
	cfg := Config{ExecFaceVenueFn: func(ctx context.Context, venue string) (map[string]string, error) {
		if fail {
			return nil, context.DeadlineExceeded
		}
		return map[string]string{"xagusdt": "perp"}, nil
	}}
	out, last := execFaceDesired(context.Background(), cfg, "okx", nil)
	if len(out) != 1 {
		t.Fatalf("首返集应有 1: %v", out)
	}
	fail = true
	out, _ = execFaceDesired(context.Background(), cfg, "okx", last)
	if len(out) != 1 || out[0] != "xagusdt" {
		t.Fatalf("err 径应保 last-good: %v", out)
	}
}

func TestExecFaceDesired_BudgetCap(t *testing.T) {
	mk := func(cap int64, ok bool) Config {
		return Config{
			ExecFaceVenueFn: func(ctx context.Context, venue string) (map[string]string, error) {
				return map[string]string{"a": "perp", "b": "perp", "c": "perp"}, nil
			},
			IndivBudgetFn: func(ctx context.Context) (int64, bool, error) { return cap, ok, nil },
		}
	}
	out, _ := execFaceDesired(context.Background(), mk(1, true), "gate", nil)
	if len(out) != 1 || out[0] != "a" {
		t.Fatalf("cap1 截断: %v", out)
	}
	out, _ = execFaceDesired(context.Background(), mk(0, false), "okx", nil)
	if out != nil {
		t.Fatalf("键缺应零订: %v", out)
	}
	out, _ = execFaceDesired(context.Background(), mk(10, true), "okx", nil)
	if len(out) != 3 {
		t.Fatalf("cap10 全放行: %v", out)
	}
}
