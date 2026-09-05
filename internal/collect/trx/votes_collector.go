// D-233 TRX 待领取实时化：周期快照器（S1）+ Vi 累积器+奖励计算器（S2）。
//
// S0 取证结论（java-tron develop 分支源码核对 2026-09-05，三项均为"是"→继续施工）：
//
//	① DECIMAL_OF_VI_REWARD = 10^18（DelegationStore.java:24: BigInteger.valueOf(10).pow(18)）
//	  Vi 语义：accumulateWitnessVi(cycle, sr, voteCount) — deltaVi = reward(cycle,sr) × 10^18 / voteCount，
//	  Vi = 前缀和（preVi + deltaVi）。reward(cycle,sr) = SR 该周期总奖励 × (1 - brokerage/100)
//	  （MortgageService.payReward：brokerage 部分进 SR allowance，余下进 delegationStore.addReward）。
//	② Vi 可由公开观测复现：TRONSCAN /api/witness annualizedRate（TRONSCAN 节点计算，含全部奖励组件）
//	  + realTimeVotes + getchainparameters → 数值积分 Vi，研究纪要实证线性拟合误差 ±0.005 TRX，
//	  协议公式复现精度更高（整数舍入级）。
//	③ 维护期记账户径：brokerage 按 cycle 存储（setBrokerage(cycle, addr, brokerage)），
//	  结算时点=周期边界（beginCycle/endCycle/currentCycle 机制，MortgageService.queryReward）。
//
// 数据流：
//
//	TRONSCAN /api/witness (10min) → annualizedRate per SR → 数值积分 deltaVi → 累积 Vi
//	→ fin_trx_cycle_vi upsert → GetHedgeStructure 现算 待领取 = allowance + Σ Vi × userVotes / 10^18
//	周期时钟=allowance 步进检测（30s 序列已实证），不依赖链上内部周期号。
package trx

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// cyclesPerYear TRON 维护期周期数（实测 ~12h/cycle → 2/日 × 365 = 730；研究纪要 §2.1 实证）。
const cyclesPerYear = 730

// cycleDurationSec 单周期时长（秒，~12h=43200s；用于按时间比例折算 deltaVi）。
const cycleDurationSec = 43200

// decimalOfViReward = 10^18（java-tron DelegationStore.DECIMAL_OF_VI_REWARD，S0 取证①）。
var decimalOfViReward = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

// VotesCollector D-233 S1+S2：周期快照器+Vi 累积器。
// 10min 采样 TRONSCAN /api/witness → annualizedRate per SR → 数值积分 Vi → fin_trx_cycle_vi upsert。
// allowance 步进（由 AccountCollector 指纹变化搭车通知）→ cycle 边界 → Vi checkpoint。
type VotesCollector struct {
	address, tronscanBase, trongridBase, apiKey string
	store                                       *pgstore.Store

	// In-memory Vi 累积状态（重启从 DB MaxTRXCycleViCycle 恢复）。
	currentCycle    int64               // 自有步进号（allowance 步进+1）
	viBySR          map[string]*big.Int // sr_address → 累积 Vi（当前 cycle）
	lastSampleAt    time.Time           // 上次采样时间（deltaVi 时间比例折算用）
	lastAllowance   int64               // 上次已知 allowance（步进检测用）
	chainParams     map[string]int64    // getchainparameters 缓存（24h）
	paramsFetchedAt time.Time           // chainParams 获取时间（24h 缓存判断）
}

// NewVotesCollector 创建周期快照器+Vi 累积器。
// tronscanBase 空=DefaultTronscanBase；trongridBase 空=DefaultBase。
func NewVotesCollector(address string, store *pgstore.Store, tronscanBase, trongridBase, apiKey string) *VotesCollector {
	if tronscanBase == "" {
		tronscanBase = DefaultTronscanBase
	}
	if trongridBase == "" {
		trongridBase = DefaultBase
	}
	return &VotesCollector{
		address:      address,
		tronscanBase: tronscanBase,
		trongridBase: trongridBase,
		apiKey:       apiKey,
		store:        store,
		viBySR:       make(map[string]*big.Int),
		chainParams:  make(map[string]int64),
	}
}

// Kind 返回采集器标识。
func (c *VotesCollector) Kind() string { return "trx_votes" }

// NotifyAllowanceStep D-233 S2：allowance 步进通知（由 AccountCollector onChange 搭车触发）。
// cycle 边界：当前 Vi checkpoint 落库 → cycle+1 → Vi 重置归零。
func (c *VotesCollector) NotifyAllowanceStep(ctx context.Context) {
	// checkpoint 当前 cycle 全部 SR Vi 到 DB（最终态落库）
	for srAddr, vi := range c.viBySR {
		if err := c.store.UpsertTRXCycleVi(ctx, pgstore.TRXCycleVi{
			Cycle:     c.currentCycle,
			SRAddress: srAddr,
			Vi:        vi,
			UpdatedAt: time.Now(),
		}); err != nil {
			log.Printf("[trx_votes] checkpoint cycle=%d sr=%s: %v", c.currentCycle, srAddr, err)
		}
	}
	c.currentCycle++
	c.viBySR = make(map[string]*big.Int)
	log.Printf("[trx_votes] cycle boundary → new cycle=%d (allowance step detected)", c.currentCycle)
}

// Poll D-233 S1+S2：采样 witness+vote → 数值积分 Vi → upsert fin_trx_cycle_vi。
func (c *VotesCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	// 首次 Poll 从 DB 恢复 currentCycle
	if c.currentCycle == 0 {
		maxCycle, err := c.store.MaxTRXCycleViCycle(ctx)
		if err != nil {
			return nil, fmt.Errorf("trx votes restore cycle: %w", err)
		}
		c.currentCycle = maxCycle // 空表=0，从 cycle 0 开始
	}

	now := time.Now()

	// 1. 拉全网 witness（annualizedRate per SR）
	witnessBody, err := tronscanGet(ctx, c.tronscanBase+"/api/witness")
	if err != nil {
		return nil, fmt.Errorf("trx votes witness: %w", err)
	}
	var witnessRows []tronscanWitnessJSON
	if err := json.Unmarshal(witnessBody, &witnessRows); err != nil {
		return nil, fmt.Errorf("trx votes witness unmarshal: %w", err)
	}

	// 2. 拉当前投票分布（/api/vote?voter={addr}）→ 用户 per-SR 票数
	voteBody, err := tronscanGet(ctx, c.tronscanBase+"/api/vote?voter="+c.address)
	if err != nil {
		log.Printf("[trx_votes] vote query failed: %v", err)
	} else {
		// voteBody 在下方解析
		_ = voteBody
	}
	var voteResp tronscanVoteResponseJSON
	if voteBody != nil {
		if err := json.Unmarshal(voteBody, &voteResp); err != nil {
			log.Printf("[trx_votes] vote unmarshal failed: %v", err)
		}
	}

	// 3. 构建 SR 地址 → annualizedRate 映射（只含有年化的 SR）
	rateByAddr := make(map[string]float64, len(witnessRows))
	for _, w := range witnessRows {
		rate, perr := strconv.ParseFloat(w.AnnualizedRate, 64)
		if perr != nil || rate <= 0 {
			continue
		}
		rateByAddr[w.Address] = rate
	}

	// 4. 检测 allowance 步进（从最新 fin_trx_account 读 allowance）
	acc, accErr := c.store.LastTRXAccount(ctx)
	if accErr == nil {
		if c.lastAllowance > 0 && acc.AllowanceSun != c.lastAllowance {
			// allowance 步进 → cycle 边界
			c.NotifyAllowanceStep(ctx)
		}
		c.lastAllowance = acc.AllowanceSun
	}

	// 5. 数值积分 deltaVi（按时间比例折算）
	// deltaVi = annualizedRate × 10^24 / (100 × cyclesPerYear) × dt / cycleDuration
	// 简化：deltaVi = annualizedRate × 10^22 × dt / (100 × cyclesPerYear × cycleDuration)
	//   [10^24 = 10^6(sun) × 10^18(DECIMAL)；但 annualizedRate 已含 % → 除 100]
	// 更简洁：per-cycle deltaVi = rate × 10^24 / (100 × cyclesPerYear)
	//         per-dt deltaVi = per-cycle × dt / cycleDuration
	if !c.lastSampleAt.IsZero() {
		dt := now.Sub(c.lastSampleAt).Seconds()
		if dt > 0 && dt < cycleDurationSec*2 { // 防异常间隔（重启后首帧 dt 巨大）
			for srAddr, rate := range rateByAddr {
				// 只累积用户投票的 SR（减少无关计算+库写入）
				userVotes := userVotesForSR(voteResp.Data, srAddr)
				if userVotes <= 0 {
					continue
				}
				// deltaVi = rate × 10^24 / (100 × cyclesPerYear) × dt / cycleDuration
				// = rate × 10^24 × dt / (100 × cyclesPerYear × cycleDuration)
				// 用 big.Int 避免浮点精度丢失：rate×dt 先算 float，再乘 10^22 转 big.Int
				// rateFloat = rate × dt / (100 × cyclesPerYear × cycleDuration)
				rateFloat := rate * dt / (100.0 * cyclesPerYear * cycleDurationSec)
				// deltaVi = rateFloat × 10^24（sun × DECIMAL）
				// big.Int 精确：rateFloat × 10^24 = rateFloat × 10^24
				scaled := new(big.Float).SetFloat64(rateFloat)
				scaled.Mul(scaled, new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil)))
				deltaVi, _ := scaled.Int(nil)
				if c.viBySR[srAddr] == nil {
					c.viBySR[srAddr] = new(big.Int)
				}
				c.viBySR[srAddr].Add(c.viBySR[srAddr], deltaVi)
			}
		}
	}
	c.lastSampleAt = now

	// 6. Upsert fin_trx_cycle_vi（当前 cycle 全部用户投票 SR 的 Vi + user_votes）
	for _, v := range voteResp.Data {
		if v.Votes <= 0 {
			continue
		}
		vi := c.viBySR[v.CandidateAddress]
		if vi == nil {
			vi = big.NewInt(0)
		}
		if err := c.store.UpsertTRXCycleVi(ctx, pgstore.TRXCycleVi{
			Cycle:     c.currentCycle,
			SRAddress: v.CandidateAddress,
			Vi:        vi,
			UserVotes: v.Votes,
			UpdatedAt: now,
		}); err != nil {
			log.Printf("[trx_votes] upsert cycle=%d sr=%s: %v", c.currentCycle, v.CandidateAddress, err)
		}
	}

	// 7. getchainparameters（24h 缓存，形状文档+交叉校验用，主算路径不依赖）
	if time.Since(c.paramsFetchedAt) > 24*time.Hour {
		c.fetchChainParameters(ctx)
	}

	return nil, nil
}

// fetchChainParameters 拉取 TronGrid /wallet/getchainparameters（匿名已实测 200，24h 缓存）。
func (c *VotesCollector) fetchChainParameters(ctx context.Context) {
	body, err := tronPost(ctx, c.trongridBase, c.apiKey, "/wallet/getchainparameters", `{}`)
	if err != nil {
		log.Printf("[trx_votes] getchainparameters failed: %v", err)
		return
	}
	var resp chainParametersResponseJSON
	if err := json.Unmarshal(body, &resp); err != nil {
		log.Printf("[trx_votes] getchainparameters unmarshal: %v", err)
		return
	}
	c.chainParams = make(map[string]int64, len(resp.ChainParameter))
	for _, p := range resp.ChainParameter {
		c.chainParams[p.Key] = p.Value
	}
	c.paramsFetchedAt = time.Now()
}

// userVotesForSR 从 vote 分布中查找用户对指定 SR 的票数。
func userVotesForSR(votes []tronscanVoteJSON, srAddr string) int64 {
	for _, v := range votes {
		if v.CandidateAddress == srAddr {
			return v.Votes
		}
	}
	return 0
}

// StartWatchdog 数据新鲜度 watchdog（trx 同款最小版，>30min 无新行 → log stale）。
func (c *VotesCollector) StartWatchdog(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			viRows, err := c.store.LatestTRXCycleVi(ctx)
			if err != nil || len(viRows) == 0 {
				continue
			}
			latest := viRows[0].UpdatedAt
			if time.Since(latest) > 30*time.Minute {
				log.Printf("[trx_votes] stale warning: no vi update for %v", time.Since(latest).Round(time.Second))
			}
		}
	}
}
