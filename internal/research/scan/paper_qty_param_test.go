// paper_qty_param_test.go —— D-546 纸面名义额参数化锚：PG 键优先/缺键兜底/破值兜底。
package scan

import (
	"context"
	"testing"

	"nox/internal/store/pgstore"
)

type qtyCfg struct {
	v  float64
	ok bool
}

func (q qtyCfg) AppConfigFloat(context.Context, string) (float64, bool, error) { return q.v, q.ok, nil }

func TestPaperQtyUSDAt(t *testing.T) {
	if got := PaperQtyUSDAt(context.Background(), qtyCfg{5000, true}); got != 5000 {
		t.Fatalf("PG 键未生效: %v", got)
	}
	if got := PaperQtyUSDAt(context.Background(), qtyCfg{0, false}); got != paperQtyUSDFallback {
		t.Fatalf("缺键未兜底: %v", got)
	}
	if got := PaperQtyUSDAt(context.Background(), qtyCfg{-1, true}); got != paperQtyUSDFallback {
		t.Fatalf("破值未兜底: %v", got)
	}
	if got := PaperQtyUSDAt(context.Background(), nil); got != paperQtyUSDFallback {
		t.Fatalf("nil store 未兜底: %v", got)
	}
}

var _ = pgstore.Store{} // 引用保 import（Store 原生实现 AppConfigFloat 由编译断言面担保）
