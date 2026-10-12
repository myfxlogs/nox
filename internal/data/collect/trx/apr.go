// TRX 投票收益率采集器（SSOT §S1）。
// 数据源：TRONSCAN 公开 API（免认证）：
//   - GET {apilist}/api/witness → 全网见证人年化收益率（max_rate 取 producer=true 降序第一）
//   - GET {apilist}/api/vote?voter={addr} → 当前投票分布（current_rate=票数加权平均年化）
//
// 落库：fin_trx_vote_apr 每轮聚合一行（288 行/日，5min 节流）。
// 纯只读：只调查询端点，无交易构造无私钥。
package trx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"nox/internal/data/fact"
	"nox/internal/store/pgstore"
)

// DefaultTronscanBase 是 TRONSCAN API 基址（免认证，与 TronLink 投票页 APR 同源）。
const DefaultTronscanBase = "https://apilist.tronscan.org"

// AprCollector 采集 TRX 投票收益率：witness 全网年化 + vote 当前投票分布 → 聚合一行。
type AprCollector struct {
	address string // 监控地址（base58，用于 /api/vote?voter=）
	store   *pgstore.Store
	base    string // TRONSCAN API 基址（可注入供测试）
	// on-change 触发去抖 + 聚合值变化发布。
	// hedgeHook nil=未接线（测试场景）；debounce 默认 60s（on-change 触发去抖，witness 204KB on-change 才拉）。
	// lastCurrentRate/lastMaxRate/lastSpread=上轮聚合值，变化才发布 hedgeHook（常规无变化轮不推）。
	hedgeHook func()
	debounce  time.Duration
	// R2 修复：调度器每源一 goroutine（sched.go:22）——trx_apr goroutine 调 Poll + trx_account goroutine
	// 经 TriggerNow 亦调 Poll，共享状态裸读写=构造性数据竞争。mu 保护 check-and-set 与状态读改段互斥。
	mu              sync.Mutex
	lastTrigger     time.Time
	lastCurrentRate float64
	lastMaxRate     float64
	lastSpread      float64
	hasLastValues   bool
}

// NewAprCollector 创建投票收益率采集器。base 空=DefaultTronscanBase。
func NewAprCollector(address string, store *pgstore.Store, base string) *AprCollector {
	if base == "" {
		base = DefaultTronscanBase
	}
	return &AprCollector{address: address, store: store, base: base, debounce: 60 * time.Second}
}

// SetHedgeHook D-231 S5：注入对冲结构刷新发布回调（聚合值变化时调用）。
func (c *AprCollector) SetHedgeHook(fn func()) { c.hedgeHook = fn }

// SetDebounce D-231 S5：设置 on-change 触发去抖间隔（测试可注小值，默认 60s）。
func (c *AprCollector) SetDebounce(d time.Duration) {
	c.mu.Lock()
	c.debounce = d
	c.mu.Unlock()
}

// TriggerNow D-231 S5：on-change 立即触发 Poll（去抖 ≥debounce）。
// 用于 AccountCollector 指纹变化时搭车触发——witness 常规 5min 节奏不变，on-change 才即时拉。
// R2 修复：check-and-set 持锁，与 Poll 状态读改段互斥（防双 goroutine 数据竞争）。
func (c *AprCollector) TriggerNow(ctx context.Context) {
	c.mu.Lock()
	if c.debounce <= 0 {
		c.debounce = 60 * time.Second
	}
	if time.Since(c.lastTrigger) < c.debounce {
		c.mu.Unlock()
		return // 去抖：两次触发间隔 ≥debounce
	}
	c.lastTrigger = time.Now()
	c.mu.Unlock()
	_, _ = c.Poll(ctx)
}

// Kind 返回采集器标识。
func (c *AprCollector) Kind() string { return "trx_apr" }

// Poll 采集 witness + vote → 聚合一行写入 fin_trx_vote_apr。返回 nil facts（P2-1 同款）。
func (c *AprCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	// 1. 拉全网 witness（年化收益率列表）
	witnessBody, err := tronscanGet(ctx, c.base+"/api/witness")
	if err != nil {
		return nil, fmt.Errorf("trx apr witness: %w", err)
	}
	var witnessRows []tronscanWitnessJSON
	if err := json.Unmarshal(witnessBody, &witnessRows); err != nil {
		return nil, fmt.Errorf("trx apr witness unmarshal: %w", err)
	}

	// 2. 构建 SR 地址 → 年化 rate 映射（用于 current 票数加权平均）
	//    同时取 max_rate/max_sr（producer=true 降序第一）
	rateByAddr := make(map[string]float64, len(witnessRows))
	var maxRate float64
	var maxSrName string
	for _, w := range witnessRows {
		rate, perr := f64z(w.AnnualizedRate)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "w.AnnualizedRate", w.AnnualizedRate, perr)
		}
		if rate <= 0 {
			continue // 畸形行跳过（年化为空/0/非数字）
		}
		rateByAddr[w.Address] = rate
		if w.Producer && rate > maxRate {
			maxRate = rate
			maxSrName = w.Name
		}
	}

	// 3. 拉当前投票分布（/api/vote?voter={addr}）
	//    S1.2 探针核真（2026-09-05）：免认证 200，返回 totalVotes + data[]（票数分布 3 个 SR）。
	//    current 口径=票数加权平均年化：Σ(votes_i × rate_i)/Σvotes_i
	//    rate_i 按候选地址对齐 witness 列表；候选不在列表内则该条跳过并计数。
	var currentRate float64
	var currentSrName string
	// 追踪已投 SR 中最低年化（业主要显示最低而非加权平均）
	var minRate float64
	var minSrName string
	var skippedCandidates int
	voteBody, err := tronscanGet(ctx, c.base+"/api/vote?voter="+c.address)
	if err != nil {
		// vote 端点失败不阻断 witness 落库（current_rate=0，max_rate 仍有值）
		log.Printf("[trx_apr] vote query failed (current_rate=0): %v", err)
	} else {
		var voteResp tronscanVoteResponseJSON
		if err := json.Unmarshal(voteBody, &voteResp); err != nil {
			log.Printf("[trx_apr] vote unmarshal failed (current_rate=0): %v", err)
		} else {
			var weightedSum, totalVotes float64
			var topVoteSr string
			var topVotes int64
			for _, v := range voteResp.Data {
				rate, ok := rateByAddr[v.CandidateAddress]
				if !ok {
					skippedCandidates++ // 候选不在 witness 列表 → 跳过并计数
					continue
				}
				weightedSum += float64(v.Votes) * rate
				totalVotes += float64(v.Votes)
				if v.Votes > topVotes {
					topVotes = v.Votes
					topVoteSr = v.CandidateName
				}
				// 追踪最低年化 SR
				if minRate == 0 || rate < minRate {
					minRate = rate
					minSrName = v.CandidateName
				}
			}
			if totalVotes > 0 {
				currentRate = weightedSum / totalVotes
				currentSrName = topVoteSr
			}
		}
	}

	// 4. 聚合一行写入 fin_trx_vote_apr
	// spread 口径改 maxRate - minRate（与 voteAprCurrent=minRate 自洽）
	rec := pgstore.TRXVoteApr{
		FetchedAt:   time.Now(),
		CurrentRate: currentRate,
		MaxRate:     maxRate,
		MinRate:     minRate,
		Spread:      maxRate - minRate,
	}
	if currentSrName != "" {
		rec.CurrentSr = &currentSrName
	}
	if maxSrName != "" {
		rec.MaxSr = &maxSrName
	}
	if minSrName != "" {
		rec.MinSr = &minSrName
	}
	if err := c.store.InsertTRXVoteApr(ctx, rec); err != nil {
		return nil, fmt.Errorf("trx apr insert: %w", err)
	}
	// 全 SR top5 producer 年化持久化（身份争议逐 SR 复核）。
	// 收集 producer=true 且 rate>0 的行，按 rate 降序取前 5，落 fin_trx_vote_apr_top。
	type srRate struct {
		name string
		rate float64
	}
	var producers []srRate
	for _, w := range witnessRows {
		if !w.Producer {
			continue
		}
		rate, perr := f64z(w.AnnualizedRate)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "w.AnnualizedRate", w.AnnualizedRate, perr)
		}
		if rate <= 0 {
			continue
		}
		producers = append(producers, srRate{name: w.Name, rate: rate})
	}
	sort.Slice(producers, func(i, j int) bool { return producers[i].rate > producers[j].rate })
	topN := 5
	if len(producers) < topN {
		topN = len(producers)
	}
	topRows := make([]pgstore.TRXVoteAprTop, 0, topN)
	for i := 0; i < topN; i++ {
		topRows = append(topRows, pgstore.TRXVoteAprTop{FetchedAt: rec.FetchedAt, SrName: producers[i].name, AnnualizedRate: producers[i].rate})
	}
	if err := c.store.InsertTRXVoteAprTopBatch(ctx, rec.FetchedAt, topRows); err != nil {
		log.Printf("[trx_apr] top5 insert failed (non-fatal): %v", err)
	}
	// 聚合值变化才发布 hedgeHook（常规无变化轮不推，客户端不做无谓全量重取）。
	// R2 修复：状态读改段持锁，与 TriggerNow check-and-set 互斥（防双 goroutine 数据竞争）。
	if c.hedgeHook != nil {
		spread := maxRate - minRate // D-297：与落库 spread 口径一致
		c.mu.Lock()
		changed := !c.hasLastValues ||
			c.lastCurrentRate != currentRate ||
			c.lastMaxRate != maxRate ||
			c.lastSpread != spread
		c.lastCurrentRate = currentRate
		c.lastMaxRate = maxRate
		c.lastSpread = spread
		c.hasLastValues = true
		c.mu.Unlock()
		if changed {
			c.hedgeHook()
		}
	}
	if skippedCandidates > 0 {
		log.Printf("[trx_apr] %d vote candidates not in witness list (skipped for weighted avg)", skippedCandidates)
	}
	return nil, nil
}

// aprStaleThreshold D-157：stale 判定阈——failsafe Poll 1h×2+margin。
// 事件驱动期（D-137 摘常驻轮询）15min 残值对 ~6h 边界节奏=常态误报（生产实证每分一条
// 连续 2h+）；真死（tronscan 挂）与常态静默无法区分。入 sources 1h 兜底调度后，
// 阈=failsafe+margin 使「stale」重新可判。
const aprStaleThreshold = 150 * time.Minute

// StartWatchdog 数据新鲜度 watchdog（D-157：>2.5h 无新行 → log stale；阈随 1h failsafe 调度）。
func (c *AprCollector) StartWatchdog(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var last time.Time
			// 空表（ErrNoRows）→ last 取零值 → Since 巨大 → 触发 stale warning
			if apr, err := c.store.LastTRXVoteApr(ctx); err == nil {
				last = apr.FetchedAt
			}
			if time.Since(last) > aprStaleThreshold {
				log.Printf("[trx_apr] stale warning: no vote apr update for %v", time.Since(last).Round(time.Second))
			}
		}
	}
}

// tronscanGet 发送 TRONSCAN GET 请求（免认证，公开 API）。
func tronscanGet(ctx context.Context, url string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tronscan GET %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
