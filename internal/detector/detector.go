// Package detector 实现机会检测器（§5）。
// 检测器是纯函数——无 I/O、无副作用，只产 Candidate。
package detector

import (
	"context"
	"sort"
	"strings"

	"arb/internal/bus"
)

// OppType 机会类型（§2.6）。
type OppType string

const (
	TypeCrossVenue    OppType = "CROSS_VENUE"
	TypeFundingHedge  OppType = "FUNDING_HEDGE"
	TypeCarry         OppType = "CARRY"
)

// Candidate 是检测器产出的原始候选（评估器消费）。
type Candidate struct {
	Type       OppType
	Canonical  string
	VenueBuy   string // "{venue}:{venue_type}"
	VenueSell  string
	PriceBuy   float64
	PriceSell  float64
	GrossBps   float64
	QuoteTime  int64 // unix milli
}

// FactProvider 提供 Fact 数据给检测器（§5.2/§5.3）。
type FactProvider interface {
	FundingFacts() map[string]map[string]float64 // canonical → venue → 年化
	SwapFacts() map[string]map[string][2]float64 // canonical → venue → [long, short]
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
