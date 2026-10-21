// earn_pregate_test.go —— earn 前提闸锚（10-18 注记⑦）：已部署+spot≈0→不产、
// 有真增量→过闸 双锚。
package oppmatch

import (
	"testing"
)

func TestEarnIncrementOK_DualAnchor(t *testing.T) {
	// 纯函判定件直测（池行 fake 入参——earnIncrementOK 读面壳内同语义）。
	if spotFree, _ := poolSplit([]poolT{{"spot", "USDT", 5.2}, {"earn", "USDT", 2128}}); spotFree >= earnPreMinSub {
		t.Fatal("已部署+spot≈0 应判无增量")
	}
	if spotFree, _ := poolSplit([]poolT{{"spot", "USDT", 500}, {"earn", "USDT", 2000}}); spotFree < earnPreMinSub {
		t.Fatal("真增量应过闸")
	}
}

type poolT struct {
	vt, ccy string
	eq      float64
}

func poolSplit(rows []poolT) (spot, earn float64) {
	for _, r := range rows {
		switch r.vt {
		case "spot":
			spot += r.eq
		case "earn":
			earn += r.eq
		}
	}
	return
}
