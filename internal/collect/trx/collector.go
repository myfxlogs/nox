// Package trx 实现 TRX 质押监控 P1 采集器（D-211，SSOT §4）。
// 3 个 Collector：account（5min）/txs（1h）/witness（24h），复用 collect.Scheduler。
// 纯只读：只调查询端点，405 族勿调，无交易构造无私钥。
//
// P2-1 Collector 接口适配：Poll 内直接写 fin_trx_* 表，返回 nil facts（§8 YAGNI）。
// 共享既有 Scheduler 时既有 Sink 收到空切片天然 no-op。
package trx

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// DefaultBase 是 TronGrid 主网 API 基址。
const DefaultBase = "https://api.trongrid.io"

// ---------- AccountCollector（5min） ----------

// AccountCollector 采集账户快照：getaccount + getaccountresource 拼合质押量。
type AccountCollector struct {
	address string
	store   *pgstore.Store
	base    string
	apiKey  string
}

// NewAccountCollector 创建账户快照采集器。address 为 base58 监控地址。
func NewAccountCollector(address string, store *pgstore.Store, base, apiKey string) *AccountCollector {
	if base == "" {
		base = DefaultBase
	}
	return &AccountCollector{address: address, store: store, base: base, apiKey: apiKey}
}

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

// ---------- TxsCollector（1h） ----------

// TxsCollector 采集交易流：/v1/transactions 滚动增量+有界翻页。
type TxsCollector struct {
	address      string
	store        *pgstore.Store
	base         string
	apiKey       string
	rentalPayers map[string]bool // TRX_RENTAL_PAYERS env 解析集
}

// NewTxsCollector 创建交易流采集器。rentalPayers 为付款方地址集（env 解析）。
func NewTxsCollector(address string, store *pgstore.Store, base, apiKey string, rentalPayers []string) *TxsCollector {
	if base == "" {
		base = DefaultBase
	}
	set := make(map[string]bool, len(rentalPayers))
	for _, a := range rentalPayers {
		if a = strings.TrimSpace(a); a != "" {
			set[a] = true
		}
	}
	return &TxsCollector{address: address, store: store, base: base, apiKey: apiKey, rentalPayers: set}
}

// Kind 返回采集器标识。
func (c *TxsCollector) Kind() string { return "trx_txs" }

// Poll 滚动增量采集交易流。从上次最新 ts 起翻页（≤10 页/poll），分类写入 fin_trx_txs。
// D-211 R5 缺口兜底：检测库内 oldest ts 远晚于账户创建时间（2023-01-01 锚）说明
// 429 断点后 lastTs 推进致早期段缺 → 强制 min_timestamp=0 重走全史（禁用增量停止）。
func (c *TxsCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	var lastTs int64
	if last, ok, err := c.store.LastTRXTxTs(ctx); err == nil && ok {
		lastTs = last.UnixMilli()
	}
	// R5 缺口检测：库内 oldest > 2024-01-01 说明早期段缺（账户 2023 年活跃）。
	// forceFullBackfill=true 时禁用增量停止 + 加 min_timestamp=0 强制全史走完。
	forceFullBackfill := false
	if lastTs > 0 {
		if first, ok, err := c.store.FirstTRXTxTs(ctx); err == nil && ok {
			accountCreationAnchor := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			if first.After(accountCreationAnchor) {
				forceFullBackfill = true
			}
		}
	}
	fetched := 0
	fingerprint := ""
	for page := 0; page < 10; page++ {
		// 止血4（2026-09-03）：TronGrid 翻页=fingerprint（meta 返回，&fingerprint= 传递）；
		// max_timestamp 是过滤器非游标（asc 下每页返回同样最老 200 条，830=83×10 实证）。
		// 走法=desc 从最新向旧翻，增量停止=页内最旧 ≤ lastTs 即追平；首拉（lastTs=0）全量走完。
		// R5：forceFullBackfill 时加 min_timestamp=0 强制全史（不过滤历史段）。
		url := fmt.Sprintf("%s/v1/accounts/%s/transactions?limit=200&order_by=block_timestamp,desc&visible=true",
			c.base, c.address)
		if forceFullBackfill {
			url += "&min_timestamp=0"
		}
		if fingerprint != "" {
			url += "&fingerprint=" + fingerprint
		}
		body, err := tronGet(ctx, c.base, c.apiKey, url)
		if err != nil {
			return nil, fmt.Errorf("trx txs fetch (page %d): %w", page, err)
		}
		var resp txsResponseJSON
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("trx txs unmarshal (page %d): %w", page, err)
		}
		if len(resp.Data) == 0 {
			break
		}
		oldest := resp.Data[len(resp.Data)-1].BlockTimestamp
		for _, tx := range resp.Data {
			if !forceFullBackfill && tx.BlockTimestamp <= lastTs {
				continue // 增量：已入库的旧交易跳过（幂等双保险；R5：forceFullBackfill 时重写早期段）
			}
			rec, ok := classifyTx(tx, c.address, c.rentalPayers)
			if !ok {
				continue // P2-3：非资金移动合约不入库
			}
			if err := c.store.UpsertTRXTx(ctx, rec); err != nil {
				return nil, fmt.Errorf("trx txs upsert: %w", err)
			}
			fetched++
		}
		if (!forceFullBackfill && oldest <= lastTs) || len(resp.Data) < 200 {
			break // 追平增量 / 尾页（R5：forceFullBackfill 时禁用增量停止，只走尾页）
		}
		fingerprint = resp.Meta.Fingerprint
		if fingerprint == "" {
			break
		}
		// 止血（2026-09-03 生产实证）：页间限速 1.5s——匿名 burst 8，10 连发翻页必 429；
		// 0.67 req/s < 恢复速率，10 页 ≈15s 安全
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
	if fetched > 0 {
		log.Printf("[trx] txs fetched %d new rows this poll", fetched)
	}
	return nil, nil
}

// ---------- WitnessCollector（24h） ----------

// WitnessCollector 采集见证人快照：listwitnesses 过滤 isJobs 行。
type WitnessCollector struct {
	address string
	store   *pgstore.Store
	base    string
	apiKey  string
}

// NewWitnessCollector 创建见证人采集器。address 用于取投票 SR 地址。
func NewWitnessCollector(address string, store *pgstore.Store, base, apiKey string) *WitnessCollector {
	if base == "" {
		base = DefaultBase
	}
	return &WitnessCollector{address: address, store: store, base: base, apiKey: apiKey}
}

// Kind 返回采集器标识。
func (c *WitnessCollector) Kind() string { return "trx_witness" }

// Poll 采集 listwitnesses 并只存 isJobs 行（在任 SR）。
func (c *WitnessCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	body, err := tronPost(ctx, c.base, c.apiKey, "/wallet/getaccount",
		fmt.Sprintf(`{"address":"%s","visible":true}`, c.address))
	if err != nil {
		return nil, fmt.Errorf("trx witness getaccount: %w", err)
	}
	var acc accountJSON
	if err := json.Unmarshal(body, &acc); err != nil {
		return nil, fmt.Errorf("trx witness getaccount unmarshal: %w", err)
	}
	if len(acc.Votes) == 0 {
		return nil, nil // 无投票
	}

	body2, err := tronPost(ctx, c.base, c.apiKey, "/wallet/listwitnesses", "{}")
	if err != nil {
		return nil, fmt.Errorf("trx listwitnesses: %w", err)
	}
	var resp witnessResponseJSON
	if err := json.Unmarshal(body2, &resp); err != nil {
		return nil, fmt.Errorf("trx listwitnesses unmarshal: %w", err)
	}
	now := time.Now()
	for _, w := range resp.Witnesses {
		if !w.IsJobs {
			continue // 只存在任 SR
		}
		var voteCount *int64
		if w.VoteCount > 0 {
			v := w.VoteCount
			voteCount = &v
		}
		var url *string
		if w.URL != "" {
			u := w.URL
			url = &u
		}
		if err := c.store.InsertTRXWitness(ctx, pgstore.TRXWitness{
			SRAddress: w.Address, URL: url, VoteCount: voteCount, FetchedAt: now,
		}); err != nil {
			return nil, fmt.Errorf("trx witness insert: %w", err)
		}
	}
	return nil, nil
}
