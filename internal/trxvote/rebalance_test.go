// D-269 S2 测试：决策引擎 Decide 纯函数（纯内存，无 DB）。
package trxvote

import (
	"testing"
)

// makeSR 构造测试用 SR。
func makeSR(addr string, brokerage int, rate float64, votes int64, producer bool, rank int) SRInfo {
	return SRInfo{
		Address:        addr,
		Name:           addr,
		AnnualizedRate: rate,
		Brokerage:      brokerage,
		RealTimeVotes:  votes,
		Producer:       producer,
		Rank:           rank,
	}
}

// TestDecide_BrokerageZeroPreferred brokerage=0% 优先于 brokerage=20%。
func TestDecide_BrokerageZeroPreferred(t *testing.T) {
	srList := []SRInfo{
		makeSR("SR20", 20, 5.0, 1000000, true, 1),
		makeSR("SR0a", 0, 4.0, 500000, true, 2),
		makeSR("SR0b", 0, 3.5, 400000, true, 3),
		makeSR("SR0c", 0, 3.0, 300000, true, 4),
	}
	cfg := DefaultConfig()
	cfg.MaxBrokerage = 0 // 严格只选 brokerage=0
	dec := Decide(RebalanceInput{
		SRRanking: srList,
		TotalTP:   10000,
		Config:    cfg,
	})
	// 目标应全选 brokerage=0 的 SR（SR0a/SR0b/SR0c），不含 SR20
	for _, v := range dec.Target {
		if v.SRAddress == "SR20" {
			t.Errorf("target contains SR20 (brokerage=20%%), should prefer brokerage=0%%")
		}
	}
	if len(dec.Target) != 3 { // MinSRs=3
		t.Errorf("target len = %d, want 3 (MinSRs)", len(dec.Target))
	}
}

// TestDecide_LowVotesPreferred 低票 SR 优先于高票 SR（出块奖励占比大）。
func TestDecide_LowVotesPreferred(t *testing.T) {
	// 两个 brokerage=0 的 producer SR，票数不同
	srList := []SRInfo{
		makeSR("SR_high", 0, 4.0, 10000000, true, 1), // 高票
		makeSR("SR_low", 0, 4.0, 100000, true, 2),    // 低票
		makeSR("SR_mid", 0, 4.0, 1000000, true, 3),
	}
	cfg := DefaultConfig()
	cfg.MinSRs = 1
	cfg.MaxSRs = 1
	dec := Decide(RebalanceInput{
		SRRanking: srList,
		TotalTP:   10000,
		Config:    cfg,
	})
	if len(dec.Target) != 1 {
		t.Fatalf("target len = %d, want 1", len(dec.Target))
	}
	// 低票 SR 应排第一（票数升序）
	if dec.Target[0].SRAddress != "SR_low" {
		t.Errorf("target[0] = %s, want SR_low (低票优先，出块奖励占比大)", dec.Target[0].SRAddress)
	}
}

// TestDecide_DiversifyN 分散 N 个（MinSRs..MaxSRs）。
func TestDecide_DiversifyN(t *testing.T) {
	srList := make([]SRInfo, 10)
	for i := range srList {
		srList[i] = makeSR("SR"+string(rune('a'+i)), 0, 4.0, 100000, true, i+1)
	}
	cfg := DefaultConfig() // MinSRs=3, MaxSRs=5
	dec := Decide(RebalanceInput{
		SRRanking: srList,
		TotalTP:   10000,
		Config:    cfg,
	})
	// 10 候选 → clamp(10, 3, 5) = 5
	if len(dec.Target) != 5 {
		t.Errorf("target len = %d, want 5 (MaxSRs, 10 candidates)", len(dec.Target))
	}
	// 等权分配：10000/5 = 2000
	for _, v := range dec.Target {
		if v.Votes != 2000 {
			t.Errorf("votes = %f, want 2000 (等权分配 10000/5)", v.Votes)
		}
	}
}

// TestDecide_YieldDropTrigger yield drop >15% 触发改投。
// calcYield 用奖励池公式：低票 producer SR 的出块奖励占比大 → yield 高。
func TestDecide_YieldDropTrigger(t *testing.T) {
	srList := []SRInfo{
		makeSR("SR_good", 0, 10.0, 100000, true, 1), // 低票 → 高出块奖励
		makeSR("SR_bad", 0, 1.0, 10000000, true, 2), // 高票 → 低出块奖励
		makeSR("SR_mid", 0, 5.0, 1000000, true, 3),
	}
	cfg := DefaultConfig()
	cfg.MinSRs = 1
	cfg.MaxSRs = 1
	// 当前投 SR_bad（高票低 yield），目标选 SR_good（低票高 yield）→ yield 大幅提升
	currentVotes := []VoteEntry{{SRAddress: "SR_bad", Votes: 10000}}
	dec := Decide(RebalanceInput{
		SRRanking:    srList,
		CurrentVotes: currentVotes,
		TotalTP:      10000,
		Config:       cfg,
	})
	if !dec.NeedRebalance {
		t.Errorf("NeedRebalance = false, want true (yield drop >15%%: current=SR_bad high votes low yield, target=SR_good low votes high yield); improvement=%.1f%%", dec.Improvement)
	}
}

// TestDecide_BetterOppTrigger better opp >20% 触发改投。
func TestDecide_BetterOppTrigger(t *testing.T) {
	srList := []SRInfo{
		makeSR("SR_best", 0, 20.0, 100000, true, 1), // 低票 → 极高 yield
		makeSR("SR_ok", 0, 5.0, 10000000, true, 2),  // 高票 → 中等 yield
		makeSR("SR_mid", 0, 4.0, 5000000, true, 3),
	}
	cfg := DefaultConfig()
	cfg.MinSRs = 1
	cfg.MaxSRs = 1
	// 当前投 SR_ok，目标投 SR_best → improvement >20%
	currentVotes := []VoteEntry{{SRAddress: "SR_ok", Votes: 10000}}
	dec := Decide(RebalanceInput{
		SRRanking:    srList,
		CurrentVotes: currentVotes,
		TotalTP:      10000,
		Config:       cfg,
	})
	if !dec.NeedRebalance {
		t.Errorf("NeedRebalance = false, want true (better opp >20%%: SR_ok→SR_best); improvement=%.1f%%", dec.Improvement)
	}
}

// TestDecide_NoCandidates 无候选（全 brokerage > MaxBrokerage）→ NeedRebalance=false。
func TestDecide_NoCandidates(t *testing.T) {
	srList := []SRInfo{
		makeSR("SR1", 50, 5.0, 100000, true, 1), // brokerage=50 > 20
		makeSR("SR2", 30, 4.0, 100000, true, 2), // brokerage=30 > 20
	}
	cfg := DefaultConfig() // MaxBrokerage=20
	dec := Decide(RebalanceInput{
		SRRanking: srList,
		TotalTP:   10000,
		Config:    cfg,
	})
	if dec.NeedRebalance {
		t.Error("NeedRebalance = true, want false (全 brokerage 超限)")
	}
	if dec.Reason == "" {
		t.Error("Reason should explain why no candidates")
	}
}

// TestDecide_TPZero TP=0 → NeedRebalance=false（防除零）。
func TestDecide_TPZero(t *testing.T) {
	srList := []SRInfo{
		makeSR("SR1", 0, 5.0, 100000, true, 1),
	}
	cfg := DefaultConfig()
	dec := Decide(RebalanceInput{
		SRRanking: srList,
		TotalTP:   0,
		Config:    cfg,
	})
	if dec.NeedRebalance {
		t.Error("NeedRebalance = true, want false (TP=0)")
	}
}

// TestDecide_NoChange 当前已是最优 → NeedRebalance=false。
func TestDecide_NoChange(t *testing.T) {
	srList := []SRInfo{
		makeSR("SR_best", 0, 10.0, 100000, true, 1), // 低票 → 最优
		makeSR("SR_ok", 0, 5.0, 10000000, true, 2),
		makeSR("SR_mid", 0, 4.0, 5000000, true, 3),
	}
	cfg := DefaultConfig()
	cfg.MinSRs = 1
	cfg.MaxSRs = 1
	// 当前已投 SR_best（低票最优，与目标一致）
	currentVotes := []VoteEntry{{SRAddress: "SR_best", Votes: 10000}}
	dec := Decide(RebalanceInput{
		SRRanking:    srList,
		CurrentVotes: currentVotes,
		TotalTP:      10000,
		Config:       cfg,
	})
	if dec.NeedRebalance {
		t.Errorf("NeedRebalance = true, want false (当前已是最优); improvement=%.1f%%", dec.Improvement)
	}
}

// TestCalcYield_BrokerageEffect brokerage 越低 yield 越高。
func TestCalcYield_BrokerageEffect(t *testing.T) {
	srList := []SRInfo{
		makeSR("SR0", 0, 5.0, 100000, true, 1),
		makeSR("SR20", 20, 5.0, 100000, true, 2),
	}
	votes0 := []VoteEntry{{SRAddress: "SR0", Votes: 10000}}
	votes20 := []VoteEntry{{SRAddress: "SR20", Votes: 10000}}
	yield0 := calcYield(votes0, srList)
	yield20 := calcYield(votes20, srList)
	if yield0 <= yield20 {
		t.Errorf("brokerage=0 yield (%.2f) should > brokerage=20 yield (%.2f)", yield0, yield20)
	}
}

// TestCalcYield_ProducerBonus producer SR 有出块奖励加成。
func TestCalcYield_ProducerBonus(t *testing.T) {
	srList := []SRInfo{
		makeSR("SR_prod", 0, 5.0, 100000, true, 1),    // producer
		makeSR("SR_noprod", 0, 5.0, 100000, false, 2), // 非 producer
	}
	votesProd := []VoteEntry{{SRAddress: "SR_prod", Votes: 10000}}
	votesNoProd := []VoteEntry{{SRAddress: "SR_noprod", Votes: 10000}}
	yieldProd := calcYield(votesProd, srList)
	yieldNoProd := calcYield(votesNoProd, srList)
	if yieldProd <= yieldNoProd {
		t.Errorf("producer yield (%.2f) should > non-producer yield (%.2f)", yieldProd, yieldNoProd)
	}
}

// D-285 S5：3 个新增测试。

// TestDecide_ZeroThreshold_NoChange BetterOppPct=0 + current==target → 不触发（验证 S3 `>` 修复）。
func TestDecide_ZeroThreshold_NoChange(t *testing.T) {
	srList := []SRInfo{
		makeSR("SR_best", 0, 5.0, 100000, true, 1),
		makeSR("SR_ok", 0, 4.0, 200000, true, 2),
		makeSR("SR_mid", 0, 3.5, 300000, true, 3),
	}
	cfg := DefaultConfig() // BetterOppPct=0.0（D-285 S2）
	cfg.MinSRs = 1
	cfg.MaxSRs = 1
	// 当前已投 SR_best（与目标一致）→ improvement=0，0 > 0 = false → 不触发
	currentVotes := []VoteEntry{{SRAddress: "SR_best", Votes: 10000}}
	dec := Decide(RebalanceInput{
		SRRanking:    srList,
		CurrentVotes: currentVotes,
		TotalTP:      10000,
		Config:       cfg,
	})
	if dec.NeedRebalance {
		t.Errorf("NeedRebalance = true, want false (BetterOppPct=0 + current==target, improvement=0, 0>0=false); improvement=%.4f%%", dec.Improvement)
	}
}

// TestDecide_ZeroThreshold_TinyImprovement BetterOppPct=0 + target 略优于 current → 触发（验证 0% 阈值生效）。
func TestDecide_ZeroThreshold_TinyImprovement(t *testing.T) {
	srList := []SRInfo{
		makeSR("SR_best", 0, 5.0, 100000, true, 1), // 低票 → 高出块奖励
		makeSR("SR_ok", 0, 4.0, 200000, true, 2),   // 中票
		makeSR("SR_mid", 0, 3.5, 300000, true, 3),
	}
	cfg := DefaultConfig() // BetterOppPct=0.0
	cfg.MinSRs = 1
	cfg.MaxSRs = 1
	// 当前投 SR_ok，目标选 SR_best → improvement > 0 → 触发（即使改善很小）
	currentVotes := []VoteEntry{{SRAddress: "SR_ok", Votes: 10000}}
	dec := Decide(RebalanceInput{
		SRRanking:    srList,
		CurrentVotes: currentVotes,
		TotalTP:      10000,
		Config:       cfg,
	})
	if !dec.NeedRebalance {
		t.Errorf("NeedRebalance = false, want true (BetterOppPct=0 + target 略优, improvement>0); improvement=%.4f%%", dec.Improvement)
	}
}

// TestCalcYield_MatchesTronscan D-285 S5：用固定 mock 数据验证修复后年化 ≈3.3%（非 12.9%）。
// 链上实测数据 2026-09-12 09:05 UTC：Nansen_ai brokerage=5/realTimeVotes=781751208/producer=true，
// CryptoChain brokerage=4/realTimeVotes=836503761/producer=true。
// 投票分布：Nansen_ai 71554 + CryptoChain 35778，TP=107332。
// srList 含 1 个 dummy SR 代表其余 ~125 SR（totalVotesTop127≈43.6B 接近全网真实总票）。
func TestCalcYield_MatchesTronscan(t *testing.T) {
	srList := []SRInfo{
		makeSR("Nansen_ai", 5, 4.0, 781751208, true, 1),
		makeSR("CryptoChain", 4, 4.0, 836503761, true, 2),
		// dummy SR 代表其余 ~125 SR，使 totalVotesTop127 接近全网真实值 ~43.6B TRX
		makeSR("SR_others", 20, 3.0, 42000000000, false, 3),
	}
	votes := []VoteEntry{
		{SRAddress: "Nansen_ai", Votes: 71554},
		{SRAddress: "CryptoChain", Votes: 35778},
	}
	yieldAbs := calcYield(votes, srList)
	annualized := yieldAbs / 107332 * 100
	if annualized < 3.2 || annualized > 3.4 {
		t.Errorf("annualized = %.4f%%, want 3.2-3.4%% (修复后不应多乘 27 倍)", annualized)
	}
}
