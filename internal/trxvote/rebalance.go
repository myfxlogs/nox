// D-269 S2：TRX 投票收益优化器决策引擎（纯函数，无 DB/IO 副作用）。
// 输入 SR 列表+当前投票+总 TP+配置 → 输出目标投票分布+是否需要改投。
// SSOT: docs/D-269设计-TRX投票收益优化器-2026-09-10-Claude.md §3
package trxvote

import (
	"fmt"
	"sort"
)

// SRInfo 是单个 SR 的信息（从 TronScan /api/witness 解析）。
type SRInfo struct {
	Address        string // base58
	Name           string
	AnnualizedRate float64 // 年化收益率 %
	Brokerage      int     // 佣金率 0-100
	RealTimeVotes  int64   // 实时得票
	Producer       bool    // true=在任 SR (top 27)
	Rank           int     // 得票排名 1-127
}

// VoteEntry 是单个 SR 的投票分配。
type VoteEntry struct {
	SRAddress string
	Votes     int64
}

// RebalanceConfig 是决策引擎配置（从 env 注入，有默认值）。
type RebalanceConfig struct {
	MinSRs             int     // 最少分散 SR 数（默认 3）
	MaxSRs             int     // 最多分散 SR 数（默认 5）
	MaxBrokerage       int     // 最大可接受 brokerage（默认 20，即 20%）
	MinRank            int     // 最低排名要求（默认 127，即 top 127 都可）
	PreferProducer     bool    // 偏好出块 SR（默认 true）
	YieldDropPct       float64 // 触发改投的收益下降阈值（默认 15%）
	BetterOppPct       float64 // 触发改投的更优机会阈值（默认 20%）
	MaxRebalancePerDay int     // 每日最大改投次数（默认 1）
}

// RebalanceInput 是决策引擎输入。
type RebalanceInput struct {
	SRRanking    []SRInfo    // TronScan /api/witness 全网 SR 列表
	CurrentVotes []VoteEntry // 当前投票分布
	TotalTP      int64       // 账户总投票权
	Config       RebalanceConfig
}

// RebalanceDecision 是决策引擎输出。
type RebalanceDecision struct {
	Target        []VoteEntry
	NeedRebalance bool
	CurrentYield  float64
	TargetYield   float64
	Improvement   float64 // (target-current)/current*100
	Reason        string
}

// DefaultConfig 返回默认配置（设计 §7）。
func DefaultConfig() RebalanceConfig {
	return RebalanceConfig{
		MinSRs:             3,
		MaxSRs:             5,
		MaxBrokerage:       20,
		MinRank:            127,
		PreferProducer:     true,
		YieldDropPct:       15.0,
		BetterOppPct:       20.0,
		MaxRebalancePerDay: 1,
	}
}

// Decide 决策算法（设计 §3.2）：
// 1. 过滤 brokerage <= MaxBrokerage && rank <= MinRank && annualizedRate > 0
// 2. 排序 brokerage 升序 → 年化降序 → 票数升序（低票优先，出块奖励占比大）
// 3. 选 top N（clamp MinSRs..MaxSRs），等权分配
// 4. calcYield 对比当前 vs 目标
// 5. needRebalance = improvement >= BetterOppPct || yield drop > YieldDropPct
func Decide(input RebalanceInput) RebalanceDecision {
	// TP=0 → 无法投票，不执行（防除零）
	if input.TotalTP <= 0 {
		return RebalanceDecision{
			NeedRebalance: false,
			Reason:        "TP=0，无投票权",
		}
	}

	// 1. 过滤候选
	candidates := filterCandidates(input.SRRanking, input.Config)
	if len(candidates) == 0 {
		return RebalanceDecision{
			NeedRebalance: false,
			Reason:        "无合格候选 SR（全 brokerage 超限或 rank 超限）",
		}
	}

	// 2. 排序：brokerage 升序 → 年化降序 → 票数升序
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Brokerage != candidates[j].Brokerage {
			return candidates[i].Brokerage < candidates[j].Brokerage
		}
		if candidates[i].AnnualizedRate != candidates[j].AnnualizedRate {
			return candidates[i].AnnualizedRate > candidates[j].AnnualizedRate
		}
		return candidates[i].RealTimeVotes < candidates[j].RealTimeVotes
	})

	// 3. 选 top N（clamp MinSRs..MaxSRs），等权分配
	n := clamp(len(candidates), input.Config.MinSRs, input.Config.MaxSRs)
	selected := candidates[:n]
	votesPerSR := input.TotalTP / int64(n)

	// 4. 构建目标投票分布
	target := make([]VoteEntry, n)
	for i, sr := range selected {
		target[i] = VoteEntry{SRAddress: sr.Address, Votes: votesPerSR}
	}

	// 5. 与当前投票对比，判断是否需要改投
	currentYield := calcYield(input.CurrentVotes, input.SRRanking)
	targetYield := calcYield(target, input.SRRanking)
	var improvement float64
	if currentYield > 0 {
		improvement = (targetYield - currentYield) / currentYield * 100
	}

	needRebalance := false
	reason := ""
	if improvement >= input.Config.BetterOppPct {
		needRebalance = true
		reason = fmt.Sprintf("更优机会 +%.1f%% (≥%.0f%%)", improvement, input.Config.BetterOppPct)
	} else if currentYield > 0 && targetYield < currentYield*(1-input.Config.YieldDropPct/100) {
		needRebalance = true
		reason = fmt.Sprintf("收益下降 %.1f%% (≥%.0f%%)", (currentYield-targetYield)/currentYield*100, input.Config.YieldDropPct)
	} else {
		reason = fmt.Sprintf("无需改投 (改善 %.1f%%)", improvement)
	}

	return RebalanceDecision{
		Target:        target,
		NeedRebalance: needRebalance,
		CurrentYield:  currentYield,
		TargetYield:   targetYield,
		Improvement:   improvement,
		Reason:        reason,
	}
}

// filterCandidates 过滤合格 SR：brokerage <= MaxBrokerage && rank <= MinRank && annualizedRate > 0。
func filterCandidates(srList []SRInfo, cfg RebalanceConfig) []SRInfo {
	out := make([]SRInfo, 0, len(srList))
	for _, sr := range srList {
		if sr.Brokerage > cfg.MaxBrokerage {
			continue
		}
		if sr.Rank > cfg.MinRank {
			continue
		}
		if sr.AnnualizedRate <= 0 {
			continue
		}
		out = append(out, sr)
	}
	return out
}

// clamp 返回 min(max(n, minV), maxV)——n < minV 取 minV，n > maxV 取 maxV。
func clamp(n, minV, maxV int) int {
	if n < minV {
		return minV
	}
	if n > maxV {
		return maxV
	}
	return n
}

// calcYield 计算给定投票分布的预期年化收益率（TRX/年，设计 §3.3）。
// 投票奖励部分 = (1-brokerage) × votes × 1345162240 / totalVotesTop127
// 出块奖励部分（仅 producer）= (1-brokerage) × votes × 84072640 / (realTimeVotes+1)
func calcYield(votes []VoteEntry, srList []SRInfo) float64 {
	if len(votes) == 0 || len(srList) == 0 {
		return 0
	}
	srByAddr := make(map[string]*SRInfo, len(srList))
	for i := range srList {
		srByAddr[srList[i].Address] = &srList[i]
	}
	// totalVotesTop127 = sum of all realTimeVotes（srList 已是 top 127）
	var totalVotesTop127 int64
	for _, sr := range srList {
		totalVotesTop127 += sr.RealTimeVotes
	}
	if totalVotesTop127 == 0 {
		return 0
	}

	var yield float64
	for _, v := range votes {
		sr := srByAddr[v.SRAddress]
		if sr == nil {
			continue
		}
		// 投票奖励部分（所有 top 127）
		voteReward := 1345162240.0 *
			(1 - float64(sr.Brokerage)/100) *
			float64(v.Votes) / float64(totalVotesTop127)
		yield += voteReward
		// 出块奖励部分（仅 top 27 SR / producer）
		if sr.Producer {
			blockReward := 84072640.0 *
				(1 - float64(sr.Brokerage)/100) *
				float64(v.Votes) / float64(sr.RealTimeVotes+1) // +1 防除零
			yield += blockReward
		}
	}
	return yield
}
