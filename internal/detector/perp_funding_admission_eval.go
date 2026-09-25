// perp_funding_admission_eval.go —— D-491 S3 判据引擎（自 perp_funding_admission.go 拆出保行数：
// pair 级八判据顺序闸 + canonical 汇总裁决。纯函数/无 I/O——证据由 Evaluate 装配注入。
package detector

import (
	"fmt"
	"math"
)

// pairVerdict 单 real-real 对判结论（status=pair 级裁决）。
type pairVerdict struct {
	a, b     string
	mean     float64 // |MeanDiff|
	sd       float64
	dir      float64 // 主导向占比
	samples  int     // min(nA,nB)
	depthMin *float64
	status   string
	note     string
}

// evalCanonical 单 canonical：全部 real-real 对判一遍 → 优先级 review > admitted >
// pending_depth > denied 汇总裁决；best_pair/指标取该状态的代表对（|均值差|最大者）。
func (e *PerpFundingAdmissionEvaluator) evalCanonical(
	canonical string, ps []FundingPairStat, legSet map[string]FundingLegStat,
	realVenues map[string]bool, tickSet map[string]bool, depthSet map[string]float64,
) AdmissionRow {
	var reviews, denies, pendings, admitteds []pairVerdict
	for _, p := range ps {
		if !realVenues[p.VenueA] || !realVenues[p.VenueB] {
			continue // 非 real-real 对不入判（sim 域属 demo 轨旁路）
		}
		v := e.evalPair(p, legSet, tickSet, depthSet)
		switch v.status {
		case AdmissionReview:
			reviews = append(reviews, v)
		case AdmissionAdmitted:
			admitteds = append(admitteds, v)
		case AdmissionPendingDepth:
			pendings = append(pendings, v)
		default:
			denies = append(denies, v)
		}
	}
	best := func(vs []pairVerdict) pairVerdict {
		b := vs[0]
		for _, v := range vs[1:] {
			if v.mean > b.mean {
				b = v
			}
		}
		return b
	}
	row := AdmissionRow{Canonical: canonical, Status: AdmissionDenied, Note: "无 real-real 达标对"}
	switch {
	case len(reviews) > 0:
		v := best(reviews)
		row = e.toRow(canonical, AdmissionReview, v)
	case len(admitteds) > 0:
		v := best(admitteds)
		row = e.toRow(canonical, AdmissionAdmitted, v)
	case len(pendings) > 0:
		v := best(pendings)
		row = e.toRow(canonical, AdmissionPendingDepth, v)
	case len(denies) > 0:
		v := best(denies)
		row = e.toRow(canonical, AdmissionDenied, v)
	}
	return row
}

func (e *PerpFundingAdmissionEvaluator) toRow(canonical, status string, v pairVerdict) AdmissionRow {
	return AdmissionRow{
		Canonical: canonical, Status: status,
		BestPair: v.a + "↔" + v.b, MeanSpread: v.mean, SpreadSD: v.sd,
		DirConsistency: v.dir, DepthMinUSD: v.depthMin, Samples: v.samples, Note: v.note,
	}
}

// evalPair 八判据顺序闸：钉顶/平线→review；样本/方向/σ/均值差/ticks→denied；
// 深度缺行或不足→pending_depth；全过→admitted。
func (e *PerpFundingAdmissionEvaluator) evalPair(
	p FundingPairStat, legSet map[string]FundingLegStat,
	tickSet map[string]bool, depthSet map[string]float64,
) pairVerdict {
	la, lb := legSet[p.VenueA], legSet[p.VenueB]
	dir := math.Max(p.DirPct, 1-p.DirPct)
	v := pairVerdict{
		a: p.VenueA, b: p.VenueB, mean: math.Abs(p.MeanDiff), sd: p.SDDiff,
		dir: dir, samples: min(la.N, lb.N),
	}
	// 钉顶防护：任一腿 |均值| ≥ pin → review（疑似顶格钉死/脏数据，禁自动放行）。
	if math.Abs(la.Mean) >= e.cfg.PinPct || math.Abs(lb.Mean) >= e.cfg.PinPct {
		v.status, v.note = AdmissionReview, fmt.Sprintf("钉顶防护：腿均值 %.1f/%.1f pp ≥ %.0f", la.Mean, lb.Mean, e.cfg.PinPct)
		return v
	}
	// 平线腿防护：σ=0 且 |均值|<0.5pp → review（疑似采集空壳，实证 gate XAUTUSDT 874 样本全零）。
	flat := func(l FundingLegStat) bool { return l.SD == 0 && math.Abs(l.Mean) < 0.5 }
	if flat(la) || flat(lb) {
		v.status, v.note = AdmissionReview, fmt.Sprintf("平线腿防护：%s 或 %s 窗内零波动近零均值", p.VenueA, p.VenueB)
		return v
	}
	if v.samples < e.cfg.MinSamples {
		v.status, v.note = AdmissionDenied, fmt.Sprintf("样本不足 %d<%d", v.samples, e.cfg.MinSamples)
		return v
	}
	if dir < e.cfg.DirPct {
		v.status, v.note = AdmissionDenied, fmt.Sprintf("方向一致性 %.2f<%.2f", dir, e.cfg.DirPct)
		return v
	}
	if v.sd > e.cfg.MaxSpreadSD {
		v.status, v.note = AdmissionDenied, fmt.Sprintf("pair 差 σ=%.1f>%.1f", v.sd, e.cfg.MaxSpreadSD)
		return v
	}
	if v.mean < e.cfg.MinNetAnnualized {
		v.status, v.note = AdmissionDenied, fmt.Sprintf("均值差 %.2f<%.2f pp", v.mean, e.cfg.MinNetAnnualized)
		return v
	}
	if !tickSet[p.VenueA] || !tickSet[p.VenueB] {
		v.status, v.note = AdmissionDenied, "无报价腿（ticks 窗内缺 perp 报价——funding 有数合约不可执行）"
		return v
	}
	dA, okA := depthSet[p.VenueA]
	dB, okB := depthSet[p.VenueB]
	if !okA || !okB {
		v.status, v.note = AdmissionPendingDepth, "深度证据缺行（已登记 depth_want 补采）"
		return v
	}
	dMin := math.Min(dA, dB)
	v.depthMin = &dMin
	if dMin < e.cfg.MinDepthUSD {
		v.status, v.note = AdmissionPendingDepth, fmt.Sprintf("深度不足 %.0f<%.0f USD（续采观察）", dMin, e.cfg.MinDepthUSD)
		return v
	}
	v.status, v.note = AdmissionAdmitted, "达标"
	return v
}
