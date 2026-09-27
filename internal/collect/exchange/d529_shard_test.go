package exchange

import (
	"slices"
	"testing"
)

// D-529 A1：分桶确定性——同输入同分桶、两半并集=全集、零重叠。
func TestShardSymbols(t *testing.T) {
	syms := []string{"ETH", "BTC", "SOL", "ADA", "DOGE"}
	a0, a1 := shardSymbols(syms, 0), shardSymbols(syms, 1)
	b0, b1 := shardSymbols(syms, 0), shardSymbols(syms, 1)
	if !slices.Equal(a0, b0) || !slices.Equal(a1, b1) {
		t.Fatal("同输入应同分桶")
	}
	if len(a0)+len(a1) != len(syms) {
		t.Fatalf("两半并集≠全集: %d+%d", len(a0), len(a1))
	}
	if a0[0] != "ADA" || a1[len(a1)-1] != "SOL" { // 排序后 [ADA BTC DOGE ETH SOL]→0=[ADA BTC DOGE] 1=[ETH SOL]
		t.Fatalf("分桶错: %v|%v", a0, a1)
	}
	// 空集/单元素边界
	if len(shardSymbols(nil, 0)) != 0 || len(shardSymbols(nil, 1)) != 0 {
		t.Fatal("空集应出空")
	}
	if len(shardSymbols([]string{"BTC"}, 1)) != 0 {
		t.Fatal("单元素 shard1 应空")
	}
}
