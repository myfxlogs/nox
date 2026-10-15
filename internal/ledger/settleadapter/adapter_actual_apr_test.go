package settleadapter

// adapter_actual_apr_test.go —— D-527 返修①锚：真 StoreDeps 过 ActualAPRWriter 断言
// （settlement 既有锚全走 aprDeps fake，生产桥断裂不可见——本断言钉真 adapter 型）。

import (
	"testing"

	"nox/internal/ledger/settlement"
)

func TestStoreDeps_ActualAPRWriter(t *testing.T) {
	if _, ok := any(New(nil)).(settlement.ActualAPRWriter); !ok {
		t.Fatal("StoreDeps 未实现 settlement.ActualAPRWriter——提案回链 actual_apr 回写死路")
	}
}
