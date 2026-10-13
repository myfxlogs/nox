// feasible.go —— D-247：投资人接触面可行性预检。请求生成前判「能不能投」——
// 不过=模板约束行休眠+机器可读原因，零投资人请求。
package oppmatch

import (
	"fmt"
	"math"
	"strings"

	"nox/internal/store/pgstore"
)

// Feasibility 预检输入（值全确定性来源：cfg 资金基数/单策略上限 + 能力账本）。
type Feasibility struct {
	CapitalBaseUSD float64         // 经营目标资金基数（D-030）
	PerStrategyCap float64         // 单策略占用上限 USD（≤0=未配置→该谓词失效不拦）
	Venues         map[string]bool // 能力账本出现的 venue 与 venue_type
	ReservePct     float64         // reserve_pct 已喂约束（0=无约束→该谓词失效不拦）
}

// NewFeasibility 能力账本+cfg→预检面（Venues=凭据 venue+venue_type+资金桶 venue 三源并集）。
func NewFeasibility(led *pgstore.CapabilityLedger, capitalBase, perStrategyCap float64) Feasibility {
	f := Feasibility{
		CapitalBaseUSD: capitalBase, PerStrategyCap: perStrategyCap,
		Venues: map[string]bool{},
	}
	for _, c := range led.Credentials {
		f.Venues[c.Venue] = true
		if c.VenueType != "" {
			f.Venues["type:"+c.VenueType] = true
		}
	}
	for _, b := range led.Buckets {
		f.Venues[b.Venue] = true
	}
	for _, c := range led.Constraints {
		if c.Subject == "reserve_pct" && c.ValueNum != nil {
			f.ReservePct = *c.ValueNum
		}
	}
	return f
}

// Check 模板裁决→可行否。谓词（命中即不可行）：
//  1. capital 条件请求额 > 单策略占用上限（占用 ≤ 资金基数×单策略上限=
//     AutoTradeMaxNotional 即资金基数口径下的单策略 cap）
//  2. capital 条件请求额 > 资金基数×(1−reserve_pct/100)（前置资金门槛突破储备约束——#10 型）
//  3. credential/permission/holding 条件 subject 为 venue/venue_type 且能力账本无任何行
//     （未知 venue 不许发请求，fail-closed）
//
// 返回 (可行, 原因)。
func (f Feasibility) Check(r Result) (bool, string) {
	for _, c := range r.Missing {
		switch c.Kind {
		case "capital":
			req := val(c.MinVal)
			if cap := effCap(f.CapitalBaseUSD, f.PerStrategyCap); req > cap {
				return false, fmt.Sprintf(
					"资金请求 %.0f USD > 单策略上限 %.0f USD（资金基数口径）", req, cap)
			}
			if f.CapitalBaseUSD > 0 && f.ReservePct > 0 &&
				req > f.CapitalBaseUSD*(100-f.ReservePct)/100 {
				return false, fmt.Sprintf(
					"资金请求 %.0f USD 突破储备约束（基数 %.0f×储备 %.0f%%）",
					req, f.CapitalBaseUSD, f.ReservePct)
			}
		case "credential", "permission", "holding":
			subj := strings.ToLower(c.Subject)
			if f.Venues[subj] || f.Venues["type:"+subj] {
				continue
			}
			return false, fmt.Sprintf("venue/能力 %s 不在能力账本", c.Subject)
		}
	}
	return true, ""
}

// DormantConstraint 预检不过→模板休眠约束行（subject dormant:<tmpl>@<venue>
// 复用 D-204 键规约；note=机器可读原因，source='m40-precheck'）。
func DormantConstraint(tmplID, venue, reason string) *pgstore.CapabilityConstraint {
	one := 1.0
	return &pgstore.CapabilityConstraint{
		Scope: "global", Subject: "dormant:" + tmplID + "@" + venue,
		ValueNum: &one, Note: reason, Source: "m40-precheck",
	}
}

// 资金基数×单策略上限换算：占用上限 = min(perStrategyCap, capitalBase)
// （cap>基数场景以基数收口，防 cfg 失调放行整基金请求）。
func effCap(capitalBase, perStrategyCap float64) float64 {
	if perStrategyCap <= 0 {
		return math.Inf(1)
	}
	if capitalBase > 0 && capitalBase < perStrategyCap {
		return capitalBase
	}
	return perStrategyCap
}
