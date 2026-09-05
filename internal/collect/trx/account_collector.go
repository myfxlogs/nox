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
	// 带宽自押 = frozenV2[BANDWIDTH].amount；能量全委托 → delegated_frozenV2_balance_for_energy
	var bandwidthStaked int64
	for _, f := range acc.FrozenV2 {
		if f.Amount != nil && (f.Type == nil || *f.Type == "" || *f.Type == "BANDWIDTH") {
			bandwidthStaked += *f.Amount
		}
	}
	energyDelegated := acc.AccountResource.DelegatedFrozenV2BalanceForEnergy

	var srAddr *string
	var votesTotal int64
	if len(acc.Votes) > 0 {
		srAddr = &acc.Votes[0].VoteAddress
		votesTotal = int64(acc.Votes[0].VoteCount)
	}
	// tronPowerLimit 交叉校验（D-210 §2 实证 votes=tronPowerLimit）
	if res.TronPowerLimit > 0 && votesTotal == 0 {
		votesTotal = res.TronPowerLimit
	}

	rec := pgstore.TRXAccount{
		Address:         c.address,
		VotesTotal:      votesTotal,
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
	fp := computeAccountFingerprint(acc.Votes, acc.Balance, bandwidthStaked+energyDelegated)
	if !c.firstPoll && fp != c.lastFingerprint && c.onChange != nil {
		c.onChange()
	}
	c.lastFingerprint, c.firstPoll = fp, false
	return nil, nil
}

// StartWatchdog 数据新鲜度 watchdog（CMC 同款最小版，15min 无新行 → log stale）。
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
		}
	}
}
