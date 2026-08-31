// Package detector 实现机会检测器（§5）。
// 检测器是纯函数——无 I/O、无副作用，只产 Candidate。
package detector

import (
	"context"
	"sort"
	"strings"

	"arb/internal/bus"
	"arb/internal/fact"
)

// OppType 机会类型（§2.6）。
type OppType string

const (
	TypeCrossVenue   OppType = "CROSS_VENUE"
	TypeFundingHedge OppType = "FUNDING_HEDGE"
	TypeCarry        OppType = "CARRY"
	TypePerpFunding  OppType = "PERP_FUNDING" // D-064 跨所 perp funding 价差
)

// Candidate 是检测器产出的原始候选（评估器消费）。
type Candidate struct {
	Type      OppType
	Canonical string
	VenueBuy  string // "{venue}:{venue_type}"
	VenueSell string
	PriceBuy  float64
	PriceSell float64
	GrossBps  float64
	QuoteTime int64 // unix milli
}

// FactProvider 提供 Fact 数据给检测器（§5.2/§5.3）。
type FactProvider interface {
	FundingFacts() map[string]map[string]float64 // canonical → venue → 年化
	SwapFacts() map[string]map[string][2]float64 // canonical → venue → [long, short]
	TradeModes() map[string]map[string]int64     // canonical → venueKey → mode（TTL 过滤后；D-060 F4）
	IsRealMap() map[string]*bool                 // D-178：venue → *bool（nil=未知）
}

// legTradable F4 可交易性腿过滤：买腿（开多）要求 LongOnly/FullAccess，卖腿（开空）要求
// ShortOnly/FullAccess；无 Fact（未知/TTL 过期）放行——venue 服务端拒单是最终权威（F5），
// 冷启动期不空白化矩阵。已知不兼容 → false（不产候选）。
func legTradable(modes map[string]map[string]int64, canonical, venueKey string, buy bool) bool {
	vm, ok := modes[canonical]
	if !ok {
		return true
	}
	mode, ok := vm[venueKey]
	if !ok {
		return true
	}
	if buy {
		return mode == fact.TradeModeLongOnly || mode == fact.TradeModeFull
	}
	return mode == fact.TradeModeShortOnly || mode == fact.TradeModeFull
}

// Detector 是检测器接口（§5）。
type Detector interface {
	Type() OppType
	Scan(ctx context.Context, snapshot map[string]map[string]bus.Quote, fp FactProvider) []Candidate
}

// ScanAll 对全部检测器执行扫描并合并候选。
func ScanAll(detectors []Detector, snapshot map[string]map[string]bus.Quote, fp FactProvider) []Candidate {
	ctx := context.Background()
	var all []Candidate
	for _, d := range detectors {
		all = append(all, d.Scan(ctx, snapshot, fp)...)
	}
	return dedup(all)
}

func dedup(cands []Candidate) []Candidate {
	best := make(map[string]Candidate)
	for _, c := range cands {
		k := dedupKey(c)
		if existing, ok := best[k]; !ok || c.GrossBps > existing.GrossBps {
			best[k] = c
		}
	}
	out := make([]Candidate, 0, len(best))
	for _, c := range best {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GrossBps > out[j].GrossBps })
	return out
}

func dedupKey(c Candidate) string {
	a, b := c.VenueBuy, c.VenueSell
	if a > b {
		a, b = b, a
	}
	return strings.Join([]string{string(c.Type), c.Canonical, a, b}, "|")
}
