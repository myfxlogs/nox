// D-231 S5：AccountCollector 从 collector.go 提取以保 collector.go ≤300 行（check-lines 软限）。
// 采集账户快照：getaccount + getaccountresource 拼合质押量 + 指纹变化回调。
package trx

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// AccountCollector 采集账户快照：getaccount + getaccountresource 拼合质押量。
type AccountCollector struct {
	address, base, apiKey string
	store                 *pgstore.Store
	onChange              func() // D-231 S5：指纹变化回调（nil=未接线）
	lastFingerprint       string
	firstPoll             bool // D-231 S5：首轮不触发（指纹未知）
	// D-233 R-D1：VotesCollector 并入 account 心跳（每 20 tick≈10min 节流采样，无独立轮询器）。
	votes     *VotesCollector
	tickCount int // 心跳计数（每 20 tick 调 votes.SampleAndStore）
}

// NewAccountCollector 创建账户快照采集器。address 为 base58 监控地址。
func NewAccountCollector(address string, store *pgstore.Store, base, apiKey string) *AccountCollector {
	if base == "" {
		base = DefaultBase
	}
	return &AccountCollector{address: address, store: store, base: base, apiKey: apiKey, firstPoll: true}
}

// SetOnChange D-231 S5：注入指纹变化回调。
func (c *AccountCollector) SetOnChange(fn func()) { c.onChange = fn }

// SetVotesCollector D-233 R-D1：注入 VotesCollector，并入 account 心跳每 20 tick 节流采样。
func (c *AccountCollector) SetVotesCollector(v *VotesCollector) { c.votes = v }

// Kind 返回采集器标识。
func (c *AccountCollector) Kind() string { return "trx_account" }

// Poll 采集账户快照并写入 fin_trx_account。返回 nil facts（P2-1）。
func (c *AccountCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	body, err := tronPost(ctx, c.base, c.apiKey, "/wallet/getaccount",
		fmt.Sprintf(`{"address":"%s","visible":true}`, c.address))
	if err != nil {
		return nil, fmt.Errorf("trx getaccount: %w", err)
	}
	var acc accountJSON
	if err := json.Unmarshal(body, &acc); err != nil {
		return nil, fmt.Errorf("trx getaccount unmarshal: %w", err)
	}

	body2, err := tronPost(ctx, c.base, c.apiKey, "/wallet/getaccountresource",
		fmt.Sprintf(`{"address":"%s","visible":true}`, c.address))
	if err != nil {
		return nil, fmt.Errorf("trx getaccountresource: %w", err)
	}
	var res resourceJSON
	if err := json.Unmarshal(body2, &res); err != nil {
		return nil, fmt.Errorf("trx getaccountresource unmarshal: %w", err)
	}

	// frozenV2 拼合（经验库 tron-frozenv2-composite-stake-reading）：
	// 带宽自押 = frozenV2[BANDWIDTH].amount；能量 = frozenV2[ENERGY].amount（自押）+ delegated_frozenV2_balance_for_energy（委托出）
	var bandwidthStaked int64
	var energySelfStaked int64
	for _, f := range acc.FrozenV2 {
		if f.Amount == nil {
			continue
		}
		if f.Type == nil || *f.Type == "" || *f.Type == "BANDWIDTH" {
			bandwidthStaked += *f.Amount
		} else if *f.Type == "ENERGY" {
			energySelfStaked += *f.Amount
		}
	}
	energyDelegated := acc.AccountResource.DelegatedFrozenV2BalanceForEnergy + energySelfStaked

	// R3 修复：votes[] 为逐 SR 票数（业主现投 3 SR 实测 6024/6023/6023≠总 18070）。
	// votesTotal=Σvotes[i].VoteCount；srAddr=票数最多者（非 votes[0]）。Σ=0 回退 tronPowerLimit。
	var srAddr *string
	var votesTotal int64
	var topVoteCount int64
	for _, v := range acc.Votes {
		vc := int64(v.VoteCount)
		votesTotal += vc
		if vc > topVoteCount {
			topVoteCount = vc
			srAddr = &v.VoteAddress
		}
	}
	if res.TronPowerLimit > 0 && votesTotal == 0 {
		votesTotal = res.TronPowerLimit
	}

	// TronPower = bandwidthStaked + energyDelegated（sun 单位，对齐 balance_sun 口径；除以 1e6 得 TRX）
	tronPowerSun := bandwidthStaked + energyDelegated

	rec := pgstore.TRXAccount{
		Address:         c.address,
		VotesTotal:      votesTotal,
		TronPower:       tronPowerSun,
		SRAddress:       srAddr,
		BalanceSun:      acc.Balance,
		AllowanceSun:    acc.Allowance,
		EnergyDelegated: energyDelegated,
		BandwidthStaked: bandwidthStaked,
		NetLimit:        res.NetLimit,
		FetchedAt:       time.Now(),
	}
	if err := c.store.InsertTRXAccount(ctx, rec); err != nil {
		return nil, fmt.Errorf("trx account insert: %w", err)
	}
	// D-231 S5：指纹变化 → onChange（首轮不触发）。
	// D-233 R5：allowance 入指纹——奖励结算步进即触发对冲卡刷新。
	fp := computeAccountFingerprint(acc.Votes, acc.Balance, bandwidthStaked+energyDelegated, acc.Allowance)
	if !c.firstPoll && fp != c.lastFingerprint && c.onChange != nil {
		c.onChange()
	}
	c.lastFingerprint, c.firstPoll = fp, false
	// D-233 R-D1：VotesCollector 并入心跳——每 20 tick（30s×20≈10min）节流采样 Vi。
	c.tickCount++
	if c.votes != nil && c.tickCount%20 == 0 {
		if err := c.votes.SampleAndStore(ctx); err != nil {
			log.Printf("[trx_account] votes SampleAndStore: %v", err)
		}
	}
	return nil, nil
}

// StartWatchdog 数据新鲜度 watchdog（CMC 同款最小版，15min 无新行 → log stale）。
// D-233 R-D1：并入 votes 陈旧检查（>30min 无 Vi 更新 → log stale），无独立 watchdog goroutine。
func (c *AccountCollector) StartWatchdog(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			last, err := c.store.LastTRXAccount(ctx)
			if err != nil {
				continue
			}
			if time.Since(last.FetchedAt) > 15*time.Minute {
				log.Printf("[trx] stale warning: no account update for %v",
					time.Since(last.FetchedAt).Round(time.Second))
			}
			// D-233 R-D1：votes 陈旧检查（并入 account watchdog，无独立 goroutine）
			if c.votes != nil {
				viRows, viErr := c.store.LatestTRXCycleVi(ctx)
				if viErr == nil && len(viRows) > 0 {
					latestVi := viRows[0].UpdatedAt
					if time.Since(latestVi) > 30*time.Minute {
						log.Printf("[trx_votes] stale warning: no vi update for %v",
							time.Since(latestVi).Round(time.Second))
					}
				}
			}
		}
	}
}
