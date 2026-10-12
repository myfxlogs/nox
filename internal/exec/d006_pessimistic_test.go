// d006_pessimistic_test.go —— D-006 S1：悲观杠杆档锚测。
// effLev=min(lev,20) 表驱动 + MarginPrecheck 主路径 max(broker,名义/20) 实证：
// broker 报告值≈0（无限杠杆户实拍形态）时名义/20 兜底拒开。
package exec

import (
	"context"
	"strings"
	"testing"
)

// effectiveLeverage 表驱动：实测低按实测、高/无限/非法按悲观档 20。
func TestD006_EffectiveLeverage(t *testing.T) {
	cases := []struct{ lev, want float64 }{
		{2e9, 20},  // Exness 无限杠杆哨兵（实拍值）
		{1000, 20}, // 高杠杆按悲观档
		{20, 20},   // 边界=档值
		{10, 10},   // 实测更低按实测
		{0, 20},    // 非法读数保守按档
		{-5, 20},
	}
	for _, c := range cases {
		if got := effectiveLeverage(c.lev); got != c.want {
			t.Fatalf("lev=%.4g want %.0f got %.0f", c.lev, c.want, got)
		}
	}
}

// 主路径双保守：broker 报 0.01（无限杠杆实拍形态）但名义/20=214.2 > avail → 拒。
func TestD006_NativePathPessimisticFloor(t *testing.T) {
	e := NewMT5Executor("v1", "tok", nil, &fakeMT5MarginClient{
		required: 0.01, freeMargin: 100, leverage: 2e9,
		ask: 4284, cs: 100, marginCcy: "XAU",
	})
	err := e.MarginPrecheck(context.Background(), Order{Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01})
	if err == nil || !strings.Contains(err.Error(), "effLev 20") {
		t.Fatalf("名义/20 兜底应拒（required=max(0.01,214.2)=214.2 > 80），got %v", err)
	}
}

// broker 报更高用更高：required=95 > avail=80 仍拒（名义/20 不掩盖 broker 高值）。
func TestD006_NativePathBrokerHigherWins(t *testing.T) {
	e := NewMT5Executor("v1", "tok", nil, &fakeMT5MarginClient{
		required: 95, freeMargin: 100, leverage: 2000,
		ask: 100, cs: 1, marginCcy: "XAU",
	})
	if err := e.MarginPrecheck(context.Background(), Order{Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01}); err == nil {
		t.Fatal("broker 95 > avail 80 应拒")
	}
}
