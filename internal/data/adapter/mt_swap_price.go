package adapter

// mt_swap_price.go D-104 P1 拆分：SwapFacts 价格/符号辅助函数（从 mt_swap_helpers.go 移出，纯移动零逻辑改）。

import (
	"fmt"
	"strings"

	"nox/internal/data/bus"
	"nox/internal/data/keydomain"
)

// summarizeMissingSyms 缺报价符号名单摘要：前 10 个 + 超出计 "…+N"（D-104 P0 返修③ R-c2）。
func summarizeMissingSyms(syms []string) string {
	if len(syms) <= 10 {
		return strings.Join(syms, ",")
	}
	return strings.Join(syms[:10], ",") + fmt.Sprintf(",…+%d", len(syms)-10)
}

// symbolSet 提取映射去重后的 canonical 集合。
func symbolSet(sm map[string]string) []string {
	seen := make(map[string]bool, len(sm))
	out := make([]string, 0, len(sm))
	for _, c := range sm {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// venuePrice 取 canonical 在本 venue 的最新 bid；缺失回退任一 venue 中价（近似换算，bps 精度足够）。
func venuePrice(snap map[string]map[string]bus.Quote, canonical, venue, venueType string) float64 {
	venues, ok := snap[canonical]
	if !ok {
		return 0
	}
	if v, ok2 := venues[keydomain.Join(venue, venueType)]; ok2 && v.Bid > 0 {
		return v.Bid
	}
	for _, v := range venues {
		if v.Mid() > 0 {
			return v.Mid()
		}
	}
	return 0
}
