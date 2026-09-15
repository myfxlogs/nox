// Package trx 实现 TRX 质押监控 P1 采集器（D-211，SSOT §4）。
// 3 个 Collector：account（30s，D-231 S5）/txs（1h）/witness（24h），复用 collect.Scheduler。
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

// ---------- TxsCollector（1h） ----------

// TxsCollector 采集交易流：/v1/transactions 滚动增量+有界翻页。
type TxsCollector struct {
	address      string
	store        *pgstore.Store
	base         string
	apiKey       string
	tronscanBase string          // D-279：TronScan cheatStatus 查询基址（默认 DefaultTronscanBase，空=跳过）
	rentalPayers map[string]bool // TRX_RENTAL_PAYERS env 解析集
	// D-258 S2：能量租金率 floor 告警（ARB_TRX_ENERGY_RATE_FLOOR_PCT，默认 8.0；0=禁用）。
	floorPct     float64
	lastAlertDay string // 内存节流：上次告警 UTC 日期（YYYY-MM-DD），1 条/天
	// D-278 S2：扳机三件套节流（各 1 条/天，内存日期戳）。
	lastEnergy30dAlertDay   string // 能量 30d 年化 <8% critical
	lastOvernightDeclineDay string // 空单隔夜 7d 衰退 >50% warning
	lastOvernightNegDay     string // 隔夜连续 2 日为负 warning
	lastPressureDay         string // 净值压力 warning
	lastS1CombinedDay       string // S1 30d 合成年化 <12% warning
	lastS1OvernightNegDay   string // S1 隔夜连续 2 日为负 warning
}

// NewTxsCollector 创建交易流采集器。rentalPayers 为付款方地址集（env 解析）。
// floorPct=能量租金率 floor 告警阈值（0=禁用）。tronscanBase 空=DefaultTronscanBase（D-296 S3 参数化）。
func NewTxsCollector(address string, store *pgstore.Store, base, apiKey, tronscanBase string, rentalPayers []string, floorPct float64) *TxsCollector {
	if base == "" {
		base = DefaultBase
	}
	if tronscanBase == "" {
		tronscanBase = DefaultTronscanBase
	}
	set := make(map[string]bool, len(rentalPayers))
	for _, a := range rentalPayers {
		if a = strings.TrimSpace(a); a != "" {
			set[a] = true
		}
	}
	return &TxsCollector{address: address, store: store, base: base, apiKey: apiKey, tronscanBase: tronscanBase, rentalPayers: set, floorPct: floorPct}
}

// fetchWithdrawAmount 调 gettransactioninfobyid 取 WithdrawBalance（vote_claim）领取额（sun）。
// 2026-09-15 业主报「—」修复：原始交易 parameter 仅 owner_address，链上权威值在 TXINFO withdraw_amount。
// 失败/缺字段=0（不阻断采集；UpsertTRXTx 按 tx_id 幂等，下轮 Poll 重试）。
func (c *TxsCollector) fetchWithdrawAmount(ctx context.Context, txID string) int64 {
	body, err := tronPost(ctx, c.base, c.apiKey, "/wallet/gettransactioninfobyid",
		fmt.Sprintf(`{"value":"%s","visible":true}`, txID))
	if err != nil {
		return 0
	}
	var info struct {
		WithdrawAmount int64 `json:"withdraw_amount"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return 0
	}
	return info.WithdrawAmount
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
	// D-279：批量查 TronScan cheatStatus（同地址最近 N 笔）
	cheatMap := c.fetchCheatStatuses(ctx, 200)
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
			rec.CheatStatus = cheatMap[tx.TxID] // D-279：官方恶意标记
			// 投票奖励领取额补全（2026-09-15 业主报「—」）：WithdrawBalance 原始交易无金额字段
			// （parameter 仅 owner_address，D-297 解析层面对此类型无源可取），链上权威值在
			// gettransactioninfobyid 的 withdraw_amount（sun）。仅解析额为 0 时富集——claims 低频，额外调用可忽略；
			// 失败不阻断采集（返回 0，UpsertTRXTx 按 tx_id 幂等，下轮重试）。
			if rec.Kind == "vote_claim" && rec.AmountSun == 0 {
				if amt := c.fetchWithdrawAmount(ctx, tx.TxID); amt > 0 {
					rec.AmountSun = amt
				}
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
		c.maybeAlertEnergyRate(ctx)
		c.checkYieldTriggers(ctx) // D-278 S1/S2：扳机只喊不动手
	}
	return nil, nil
}

// fetchCheatStatuses D-279：批量查 TronScan /api/transaction 获取 cheatStatus。
// 返回 txID → cheatStatus 映射；失败或 tronscanBase 空=返回 nil（不阻断采集）。
func (c *TxsCollector) fetchCheatStatuses(ctx context.Context, limit int) map[string]bool {
	if c.tronscanBase == "" {
		return nil
	}
	url := fmt.Sprintf("%s/api/transaction?sort=-timestamp&count=true&limit=%d&start=0&address=%s",
		c.tronscanBase, limit, c.address)
	body, err := tronGet(ctx, c.tronscanBase, "", url)
	if err != nil {
		log.Printf("[trx] cheatStatus fetch skipped: %v", err)
		return nil
	}
	var resp tronScanTxsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		log.Printf("[trx] cheatStatus unmarshal skipped: %v", err)
		return nil
	}
	m := make(map[string]bool, len(resp.Data))
	for _, tx := range resp.Data {
		m[tx.Hash] = tx.CheatStatus
	}
	return m
}

// maybeAlertEnergyRate D-258 S2：有新行时计算 7d 能量租金年化，低于 floor → warning 告警。
// 节流 1 条/天（内存日期戳）；样本 <7d 或 floor=0 不评估。日志诚实：触发与未触发各一行。
func (c *TxsCollector) maybeAlertEnergyRate(ctx context.Context) {
	if c.floorPct <= 0 {
		return // 禁用
	}
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -7)
	rentalSun, err := c.store.SumTRXTxRentalByRange(ctx, from, now)
	if err != nil {
		log.Printf("[trx-yield] energy rate check skipped: sum range: %v", err)
		return
	}
	firstTs, ok, err := c.store.FirstTRXTxRentalTsInRange(ctx, from, now)
	if err != nil || !ok {
		log.Printf("[trx-yield] energy rate check skipped: no rental data in 7d (sampleDays=0 floor=%.1f)", c.floorPct)
		return
	}
	sampleDays := int(now.Sub(firstTs)/(24*time.Hour)) + 1
	if sampleDays < 7 {
		log.Printf("[trx-yield] energy rate check skipped: sampleDays=%d < 7 (floor=%.1f)", sampleDays, c.floorPct)
		return
	}
	acct, err := c.store.LastTRXAccount(ctx)
	if err != nil {
		log.Printf("[trx-yield] energy rate check skipped: last account: %v", err)
		return
	}
	if acct.EnergyDelegated == 0 {
		log.Printf("[trx-yield] energy rate check skipped: energy_delegated=0 (floor=%.1f)", c.floorPct)
		return
	}
	// 9-9 过渡期护栏：7d 窗内 energy_delegated 变化>20% → 分母失真（旧租金÷新在押），floor 评估无效。
	minE, maxE, err := c.store.EnergyDelegatedRangeInWindow(ctx, from, now)
	if err == nil && minE > 0 && maxE > minE*12/10 {
		log.Printf("[trx-yield] energy rate check skipped: transition window (delegated %.0f→%.0f TRX, >20%% change)", float64(minE)/1e6, float64(maxE)/1e6)
		return
	}
	rentalTRX := float64(rentalSun) / 1e6
	energyTRX := float64(acct.EnergyDelegated) / 1e6
	apr := (rentalTRX / float64(sampleDays) * 365) / energyTRX * 100
	if apr >= c.floorPct {
		log.Printf("[trx-yield] energy rate ok: apr=%.2f%% floor=%.1f%% sampleDays=%d", apr, c.floorPct, sampleDays)
		return
	}
	today := now.Format("2006-01-02")
	if c.lastAlertDay == today {
		log.Printf("[trx-yield] energy rate below floor but already alerted today: apr=%.2f%% floor=%.1f%%", apr, c.floorPct)
		return
	}
	msg := fmt.Sprintf("[trx-yield] 能量租金率 %.2f%% < 参考线 %.1f%%（7d 实测，在押 %.0f TRX）——真实收益率跌破线，需复核质押模式是否维持",
		apr, c.floorPct, energyTRX)
	if err := c.store.InsertAlert(ctx, nil, "warn", msg); err != nil {
		log.Printf("[trx-yield] alert insert failed: %v", err)
		return
	}
	c.lastAlertDay = today // D-258 复审修正：成功后再置位——失败不消耗当日额度（可重试）
	log.Printf("[trx-yield] energy rate below floor: apr=%.2f%% floor=%.1f%% sampleDays=%d → alert sent", apr, c.floorPct, sampleDays)
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
