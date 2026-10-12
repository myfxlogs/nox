// earn APR 判断引擎——把 APR 数字判成 opportunity/not_opportunity/trap + reasons。
// 判断结果只入库+前端展示，不发告警（D-271 R5 实证告警=噪音）；独立于套利 engine/evaluator（earn 无对手腿）。
// 依赖方向：EarnJudgment 是 cexscan 内部类型；pgstore 用 CexEarnJudgment，scheduler 转换（pgstore 不导入 cexscan）。
package cexscan

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"nox/internal/store/pgstore"
)

// EarnJudgment earn APR 判断结果（cexscan 内部类型）。
type EarnJudgment struct {
	Venue       string
	Ccy         string
	ProductType string
	APR         float64
	Category    string // "stable" | "token"
	Verdict     string // "opportunity" | "not_opportunity" | "trap"
	Reasons     []string
}

// EarnJudge earn APR 判断引擎。
type EarnJudge struct {
	holdings         map[string]map[string]float64 // venue → ccy → qty（buildHoldingsFromSnapshot 三条件过滤后提取）
	transferCostPct  float64                       // 搬砖成本阈值（0.005=0.5% 一次性成本；保守启发式：假设资金停留 >6 个月，设计 §3.2）
	bearishThreshold float64                       // 看空信号阈值（0.5=50%）；历史观察经验值，后续可配置（设计 §3.3）
}

// NewEarnJudge 构造判断引擎。holdings 从 AccountMonitor 快照提取（buildHoldingsFromSnapshot）。
func NewEarnJudge(holdings map[string]map[string]float64) *EarnJudge {
	return &EarnJudge{
		holdings:         holdings,
		transferCostPct:  0.005,
		bearishThreshold: 0.5,
	}
}

// Judge 对一批 earn APR 输出判断结果。
func (j *EarnJudge) Judge(ctx context.Context, aprs []pgstore.CexEarnAPR) ([]EarnJudgment, error) {
	// 逆回购基准（GC007/repo_rates）随 cnfi/repo 面全摘除——
	// 稳定币判断仅剩跨所对比路径（无基准降级形态即新常态）。

	// 跨所索引：ccy → venue → APR（仅稳定币参与跨所对比）
	crossVenueAPR := make(map[string]map[string]float64)
	for _, apr := range aprs {
		if !isStablecoin(apr.Ccy) {
			continue
		}
		if crossVenueAPR[apr.Ccy] == nil {
			crossVenueAPR[apr.Ccy] = make(map[string]float64)
		}
		crossVenueAPR[apr.Ccy][apr.Venue] = apr.APR
	}

	out := make([]EarnJudgment, 0, len(aprs))
	for _, apr := range aprs {
		if isStablecoin(apr.Ccy) {
			out = append(out, j.judgeStable(apr, crossVenueAPR))
		} else {
			out = append(out, j.judgeToken(apr))
		}
	}
	return out, nil
}

// judgeStable 稳定币判断：跨所对比（D-139：逆回购基准路径已摘），verdict 按设计 §3.2 优先级表合并。
func (j *EarnJudge) judgeStable(apr pgstore.CexEarnAPR, crossVenueAPR map[string]map[string]float64) EarnJudgment {
	out := EarnJudgment{Venue: apr.Venue, Ccy: apr.Ccy, ProductType: apr.ProductType, APR: apr.APR, Category: "stable"}

	// 路径 B：跨所对比——abs(diff) 不固定方向（P2-1），reason 标哪所高
	crossCandidate := false
	for venue, otherAPR := range crossVenueAPR[apr.Ccy] {
		if venue == apr.Venue {
			continue
		}
		if diff := math.Abs(apr.APR - otherAPR); diff > j.transferCostPct {
			crossCandidate = true
			if apr.APR > otherAPR {
				out.Reasons = append(out.Reasons, fmt.Sprintf("跨所投放候选（本所高 %.2f%%）", diff*100))
			} else {
				out.Reasons = append(out.Reasons, fmt.Sprintf("跨所投放候选（%s 高 %.2f%%）", venue, diff*100))
			}
			break
		}
	}

	// 优先级表（设计 §3.2，D-139 基准路径摘后剩跨所）：投放候选→opportunity；其余→not_opportunity。稳定币永不判 trap。
	switch {
	case crossCandidate:
		out.Verdict = "opportunity"
	default:
		out.Verdict = "not_opportunity"
	}

	out.Reasons = append(out.Reasons, "flexible 不锁定", "假设长期持有（搬砖成本为保守启发式）")
	return out
}

// judgeToken 代币判断：持币判断 + 看空信号（设计 §3.3）。
func (j *EarnJudge) judgeToken(apr pgstore.CexEarnAPR) EarnJudgment {
	out := EarnJudgment{Venue: apr.Venue, Ccy: apr.Ccy, ProductType: apr.ProductType, APR: apr.APR, Category: "token"}

	// 持币判断浮点阈值：qty > 1e-8 才算持有
	held := j.holdings[apr.Venue][apr.Ccy] > 1e-8
	switch {
	case held && apr.APR > 0:
		out.Verdict = "opportunity"
		out.Reasons = append(out.Reasons, "已持有，可存入")
	case held:
		out.Verdict = "not_opportunity"
		out.Reasons = append(out.Reasons, "持有但负收益")
	default:
		out.Verdict = "not_opportunity"
		out.Reasons = append(out.Reasons, "不持有，需买币，币价风险")
	}
	if apr.APR > j.bearishThreshold {
		// 看空信号是推断不是事实，措辞用"可能"（P3-4）
		out.Reasons = append(out.Reasons, fmt.Sprintf("高借币利率（APR=%.0f%%），可能是做空需求信号", apr.APR*100))
		if held {
			out.Verdict = "opportunity"
			out.Reasons = append(out.Reasons, "高 APR，注意做空信号")
		} else {
			out.Verdict = "trap"
		}
	}
	return out
}

// toJSONString reasons 序列化为 JSON 数组字符串（入库 fin_cex_earn_judgment.reasons text）。
func toJSONString(ss []string) string {
	b, err := json.Marshal(ss)
	if err != nil {
		return "[]"
	}
	return string(b)
}
