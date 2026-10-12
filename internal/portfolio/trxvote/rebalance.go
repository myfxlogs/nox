// TRX 投票收益优化器决策引擎（纯函数，无 DB/IO 副作用）。
// 输入 SR 列表+当前投票+总 TP+配置 → 输出目标投票分布+是否需要改投。
// SSOT: docs/D-269设计-TRX投票收益优化器-2026-09-10-Claude.md §3
package trxvote

import (
	"fmt"
	"sort"
	"time"
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

// VoteEntry 是单个 SR 的投票分配（TRX 单位，浮点）。
type VoteEntry struct {
	SRAddress string
	Votes     float64
}

// RebalanceConfig 是决策引擎配置（从 env 注入，有默认值）。
type RebalanceConfig struct {
	MinSRs       int // 最少分散 SR 数（默认 3）
	MaxSRs       int // 最多分散 SR 数（默认 3，D-355）
	MaxBrokerage int // 最大可接受 brokerage（默认 20，即 20%）
	MinRank      int // 最低排名要求（默认 127，即 top 127 都可）
	// 改用 SpreadThreshold 触发，保留 env 兼容
	YieldDropPct       float64 // 触发改投的收益下降阈值（默认 15%）
	BetterOppPct       float64 // 触发改投的更优机会阈值（默认 20%）
	MaxRebalancePerDay int     // 每日最大改投次数（默认 1）
	// TickInterval 字段已随 30min 盲醒 tick 摘除（改投=边界事件驱动）。
	MinInterval     time.Duration // D-293 S3：防抖间隔（距上次 executed <MinInterval → 跳过，默认 2h）
	SpreadThreshold float64       // D-297：价差触发改投阈值（pp，默认 0.001）
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
		MaxSRs:             3,
		MaxBrokerage:       20,
		MinRank:            127,
		YieldDropPct:       15.0,
		BetterOppPct:       0.0,
		MaxRebalancePerDay: 1,
		MinInterval:        2 * time.Hour, // D-293 S3：防抖 2h（env ARB_TRX_VOTE_MIN_INTERVAL_MINUTES 可调）
		SpreadThreshold:    0.001,         // D-297：价差触发改投阈值 0.001pp
	}
}

// Decide 决策算法（设计 §3.2）：
// 1. 过滤 brokerage <= MaxBrokerage && rank <= MinRank && annualizedRate > 0
// 2. 排序：年化降序 → brokerage 升序 → 票数升序（低票优先，出块奖励占比大）
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

	// 2. 排序：年化降序（净 APR 主键，D-355）→ brokerage 升序 → 票数升序
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].AnnualizedRate != candidates[j].AnnualizedRate {
			return candidates[i].AnnualizedRate > candidates[j].AnnualizedRate
		}
		if candidates[i].Brokerage != candidates[j].Brokerage {
			return candidates[i].Brokerage < candidates[j].Brokerage
		}
		return candidates[i].RealTimeVotes < candidates[j].RealTimeVotes
	})

	// 3. 选 top N（clamp MinSRs..MaxSRs，但不超过候选数），等权分配
	n := clamp(len(candidates), input.Config.MinSRs, input.Config.MaxSRs)
	if n > len(candidates) {
		n = len(candidates) // 候选不足时用全部
	}
	if n <= 0 {
		return RebalanceDecision{NeedRebalance: false, Reason: "no candidates"}
	}
	selected := candidates[:n]
	votesPerSR := float64(input.TotalTP) / float64(n)

	// 4. 构建目标投票分布
	target := make([]VoteEntry, n)
	for i, sr := range selected {
		target[i] = VoteEntry{SRAddress: sr.Address, Votes: votesPerSR}
	}

	// 5. 与当前投票对比，判断是否需要改投
	// calcYield 返回绝对收益量（TRX/年），除以 TotalTP（TRX）×100 得年化百分比。
	currentYieldAbs := calcYield(input.CurrentVotes, input.SRRanking)
	targetYieldAbs := calcYield(target, input.SRRanking)
	var currentYield, targetYield float64
	if input.TotalTP > 0 {
		currentYield = currentYieldAbs / float64(input.TotalTP) * 100
		targetYield = targetYieldAbs / float64(input.TotalTP) * 100
	}
	var improvement float64
	if currentYield > 0 {
		improvement = (targetYield - currentYield) / currentYield * 100
	}

	needRebalance := false
	reason := ""
	// 价差驱动——(未投候选最高年化 − 已投最低年化) >= SpreadThreshold → 触发改投
	// D1：用 candidates（filterCandidates 后）非 SRRanking 全网
	// D2：用 maxNotVotedRate（未投候选最高），非 maxAvailable（含已投）——
	//   已投最优 SR 时 maxNotVoted < minVoted → spread < 0 → 不触发，防每日空转
	votedSet := make(map[string]bool)
	for _, v := range input.CurrentVotes {
		votedSet[v.SRAddress] = true
	}
	var maxNotVotedRate float64
	for _, sr := range candidates {
		if !votedSet[sr.Address] && sr.AnnualizedRate > maxNotVotedRate {
			maxNotVotedRate = sr.AnnualizedRate
		}
	}
	var minVotedRate float64
	for _, v := range input.CurrentVotes {
		for _, sr := range candidates {
			if sr.Address == v.SRAddress {
				if minVotedRate == 0 || sr.AnnualizedRate < minVotedRate {
					minVotedRate = sr.AnnualizedRate
				}
				break
			}
		}
	}
	spread := maxNotVotedRate - minVotedRate
	needRebalance = spread >= input.Config.SpreadThreshold
	if needRebalance {
		reason = fmt.Sprintf("价差 +%.4fpp (≥%.3fpp)，改投更高年化 SR", spread, input.Config.SpreadThreshold)
	} else {
		reason = fmt.Sprintf("无需改投 (价差 %.4fpp < %.3fpp)", spread, input.Config.SpreadThreshold)
	}
	// D3：no-op 守卫——目标 SR 集合 == 当前 SR 集合 → 跳过（避免浪费带宽）
	if needRebalance && sameSRSet(target, input.CurrentVotes) {
		needRebalance = false
		reason = "目标 SR 集合与当前一致，跳过（避免 no-op 浪费带宽）"
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

// sameSRSet 判断目标投票分布与当前投票分布的 SR 集合是否相同（不管票数）。
func sameSRSet(target, current []VoteEntry) bool {
	if len(target) != len(current) {
		return false
	}
	set := make(map[string]bool, len(current))
	for _, v := range current {
		set[v.SRAddress] = true
	}
	for _, v := range target {
		if !set[v.SRAddress] {
			return false
		}
	}
	return true
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
// 出块奖励部分（仅 producer）= (1-brokerage) × votes × 3113800 / (realTimeVotes+1)
// 3113800 = 每 SR 年出块奖励池（设计 §收益模型：日出块 3113800 × 365）。
// 注意：84072640 = 3113800 × 27 是全网 27 SR 总和，不能直接用于单 SR（D-285 修复）。
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
			blockReward := 3113800.0 *
				(1 - float64(sr.Brokerage)/100) *
				float64(v.Votes) / float64(sr.RealTimeVotes+1) // +1 防除零
			yield += blockReward
		}
	}
	return yield
}
