// D-269 S4：TRX 投票收益优化器调度器（6h rebalance + 24h claim）。
// SSOT: docs/D-269设计-TRX投票收益优化器-2026-09-10-Claude.md §5
package trxvote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/fbsobreira/gotron-sdk/pkg/account"
)

// AccountFetcher 获取账户详情（当前投票+总 TP），便于 mock 测试。
type AccountFetcher interface {
	GetAccountDetailedCtx(ctx context.Context, addr string) (*account.Account, error)
}

// Scheduler 调度 TRX 投票收益优化器的两个独立 tick。
type Scheduler struct {
	executor        *Executor
	store           Store
	address         string
	tronscanBase    string
	config          RebalanceConfig
	account         AccountFetcher // 获取当前投票+总 TP
	now             func() time.Time
	lastClaimFailAt time.Time // D-293 S2：领奖阻塞冷却（失败/带宽不足共用，15min，防 60s 周期重复告警轰炸）
	aboveThreshold  bool      // D-293 R1 P3-1：reward 是否在 >10e6 区间（状态迁移才打日志，避免每 60s 刷屏）
}

// Store 是 Scheduler 依赖的存储接口（解耦 pgstore.Store 供测试注入）。
type Store interface {
	InsertTrxVoteRebalance(ctx context.Context, rec TRXVoteRebalanceRecord) (int64, error)
	UpdateTrxVoteRebalanceResult(ctx context.Context, id int64, txHash, status string) error
	TrxVoteRebalancedToday(ctx context.Context, maxPerDay int) bool
	LastExecutedRebalanceAt(ctx context.Context) (time.Time, error) // D-293 S3：防抖查询
}

// TRXVoteRebalanceRecord 是 Store.InsertTrxVoteRebalance 的输入（解耦 pgstore struct 供测试）。
type TRXVoteRebalanceRecord struct {
	CurrentYield   float64
	TargetYield    float64
	ImprovementPct float64
	NeedRebalance  bool
	Reason         string
	TargetVotes    []byte // JSONB
	Status         string
}

// NewScheduler 构造调度器。
func NewScheduler(executor *Executor, store Store, address, tronscanBase string, config RebalanceConfig, account AccountFetcher) *Scheduler {
	return &Scheduler{
		executor:     executor,
		store:        store,
		address:      address,
		tronscanBase: tronscanBase,
		config:       config,
		account:      account,
		now:          time.Now,
	}
}

// SetNow 替换 now 函数（测试用）。
func (s *Scheduler) SetNow(fn func() time.Time) { s.now = fn }

// Run 启动改投 tick（D-293 S2：领奖删 24h 槽位，改搭车 60s 采集流→ClaimCheck）。
// - 改投 tick：每 30min（boot 后等 5min 首次执行）—— S3 改 6h→30min
func (s *Scheduler) Run(ctx context.Context) {
	go s.rebalanceLoop(ctx)
}

// rebalanceLoop 每 TickInterval 执行改投决策（D-293 S3：6h→30min 参数化）。
func (s *Scheduler) rebalanceLoop(ctx context.Context) {
	interval := s.config.TickInterval
	if interval <= 0 {
		interval = 30 * time.Minute // 默认 30min
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	time.Sleep(5 * time.Minute) // boot 后等 5min 首次执行
	s.doRebalance(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.doRebalance(ctx)
		}
	}
}

// ClaimCheck 领奖条件触发（D-293 S2：搭车 60s 采集流，删 24h 槽位）。
// 三态：rewardSun ≤10e6 → 零 RPC 跳过；失败/带宽不足冷却 <15min → 跳过；否则带宽预检→ClaimReward。
// 由 account_collector 每轮采到 rewardNumSun 后调用（hook recover 包裹防拖垮采集主循环）。
func (s *Scheduler) ClaimCheck(ctx context.Context, rewardSun int64) {
	// 1. 奖励不足 10 TRX → 零 RPC 跳过（D-293 R1 P3-1：仅状态迁移时打日志，避免每 60s 刷屏）
	if rewardSun <= 10_000_000 {
		if s.aboveThreshold {
			s.aboveThreshold = false
			log.Printf("[trxvote] claim check: reward=%d sun ≤10e6, entered below-threshold (skip)", rewardSun)
		}
		return
	}
	if !s.aboveThreshold {
		s.aboveThreshold = true
		log.Printf("[trxvote] claim check: reward=%d sun >10e6, entered above-threshold", rewardSun)
	}
	// 2. 冷却：距上次领奖阻塞（失败/带宽不足）<15min → 跳过（防 60s 周期重复告警轰炸）
	if !s.lastClaimFailAt.IsZero() && s.now().Sub(s.lastClaimFailAt) < 15*time.Minute {
		log.Printf("[trxvote] claim check: cooling down (last block %v ago), skip", s.now().Sub(s.lastClaimFailAt).Round(time.Second))
		return
	}
	// 3. 带宽预检（领奖 ~250 BP）
	ok, err := s.executor.HasBandwidth(ctx, BandwidthNeedClaimBP)
	if err != nil {
		log.Printf("[trxvote] claim bandwidth check: %v", err)
		s.executor.Alert("warn", "[投票领奖带宽查询失败] "+err.Error())
		s.lastClaimFailAt = s.now()
		return
	}
	if !ok {
		log.Println("[trxvote] 带宽不足，跳过领奖（避免烧 TRX）")
		s.executor.Alert("warn", "[投票领奖跳过] 带宽不足，等待恢复")
		s.lastClaimFailAt = s.now() // D-293 R1 P2-1：带宽不足同样设冷却，防每 60s 告警轰炸
		return
	}
	// 4. 执行领奖
	txHash, amount, err := s.executor.ClaimReward(ctx)
	if err != nil {
		log.Printf("[trxvote] claim failed: %v", err)
		s.executor.Alert("critical", "[投票领奖失败] "+err.Error())
		s.lastClaimFailAt = s.now()
		return
	}
	if txHash == "" {
		log.Println("[trxvote] claim: reward dropped below threshold, skip")
		return
	}
	log.Printf("[trxvote] claim success: tx=%s amount=%d sun (%.4f TRX)", txHash, amount, float64(amount)/1e6)
	s.executor.Alert("warn", formatClaimAlert(txHash, amount))
}

// doRebalance 执行一轮改投决策（设计 §5 v1.1）。
func (s *Scheduler) doRebalance(ctx context.Context) {
	// 1. 检查今日是否已改投
	if s.store.TrxVoteRebalancedToday(ctx, s.config.MaxRebalancePerDay) {
		log.Println("[trxvote] 今日已改投，跳过")
		return
	}

	// 1.1 D-293 S3：防抖——距上次 executed < MinInterval → 跳过（不落库，与今日已改投同位置）
	if s.config.MinInterval > 0 {
		lastExec, err := s.store.LastExecutedRebalanceAt(ctx)
		if err == nil && !lastExec.IsZero() && s.now().Sub(lastExec) < s.config.MinInterval {
			log.Printf("[trxvote] 距上次改投 %v < MinInterval %v，跳过（防抖）",
				s.now().Sub(lastExec).Round(time.Second), s.config.MinInterval)
			return
		}
	}

	// 2. 拉 SR 列表（独立 HTTP fetch TronScan /api/witness）
	srList, err := fetchSRList(ctx, s.tronscanBase)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			log.Printf("[trxvote] fetch SR list canceled (container shutdown)")
			return
		}
		log.Printf("[trxvote] fetch SR list: %v", err)
		s.executor.Alert("warn", "[投票采集失败] "+err.Error())
		return
	}

	// 3. 拉当前投票分布 + 总 TP
	accDetail, err := s.account.GetAccountDetailedCtx(ctx, s.address)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			log.Printf("[trxvote] get account detail canceled (container shutdown)")
			return
		}
		log.Printf("[trxvote] get account detail: %v", err)
		s.executor.Alert("warn", "[投票账户查询失败] "+err.Error())
		return
	}
	currentVotes := convertVotes(accDetail.Votes)
	tp := accDetail.TronPower

	// 4. 决策
	decision := Decide(RebalanceInput{
		SRRanking:    srList,
		CurrentVotes: currentVotes,
		TotalTP:      tp,
		Config:       s.config,
	})

	// 5. 落库决策记录（无论是否执行）
	targetJSON, _ := json.Marshal(decision.Target)
	rec := TRXVoteRebalanceRecord{
		CurrentYield:   decision.CurrentYield,
		TargetYield:    decision.TargetYield,
		ImprovementPct: decision.Improvement,
		NeedRebalance:  decision.NeedRebalance,
		Reason:         decision.Reason,
		TargetVotes:    targetJSON,
		Status:         "pending",
	}
	id, err := s.store.InsertTrxVoteRebalance(ctx, rec)
	if err != nil {
		log.Printf("[trxvote] insert rebalance record: %v", err)
		return
	}

	// 6. 执行（仅在 needRebalance=true 时）
	if !decision.NeedRebalance {
		log.Printf("[trxvote] 无需改投: %s", decision.Reason)
		return
	}

	// 6.1 带宽预检：改投需要 ~500 BP，不足则跳过避免烧 TRX
	ok, err := s.executor.HasBandwidth(ctx, BandwidthNeedVoteBP)
	if err != nil {
		log.Printf("[trxvote] bandwidth check: %v", err)
		s.executor.Alert("warn", "[投票带宽查询失败] "+err.Error())
		return
	}
	if !ok {
		log.Println("[trxvote] 带宽不足，跳过改投（避免烧 TRX）")
		s.executor.Alert("warn", "[投票改投跳过] 带宽不足，等待恢复")
		return
	}

	txHash, err := s.executor.Vote(ctx, decision.Target)
	if err != nil {
		log.Printf("[trxvote] rebalance failed: %v", err)
		s.executor.Alert("critical", "[投票改投失败] "+err.Error())
		return
	}
	if err := s.store.UpdateTrxVoteRebalanceResult(ctx, id, txHash, "executed"); err != nil {
		log.Printf("[trxvote] update rebalance result: %v", err)
	}
	log.Printf("[trxvote] rebalance success: tx=%s yield %s→%s TRX/yr (+%s%%) SR %s→%s",
		txHash, formatFloat(decision.CurrentYield), formatFloat(decision.TargetYield),
		formatFloat(decision.Improvement), topSRPrefix(currentVotes), topSRPrefix(decision.Target))
	s.executor.Alert("warn", formatRebalanceAlert(txHash, decision, currentVotes))
}

// convertVotes 将 map[string]int64（gotron-sdk Votes，TRX 单位）转为 []VoteEntry。
// TRON 链上 vote_count 单位是 TRX（1 TRX = 1 vote），gotron-sdk 未做转换，已是 TRX。
func convertVotes(votes map[string]int64) []VoteEntry {
	out := make([]VoteEntry, 0, len(votes))
	for addr, v := range votes {
		out = append(out, VoteEntry{SRAddress: addr, Votes: float64(v)})
	}
	return out
}

// formatRebalanceAlert 格式化改投告警（设计 §8，D-293 S4 加 SR 变化明细）。
// 旧/新 top SR 地址各取前 8 字符，便于一眼看出改投方向。
func formatRebalanceAlert(txHash string, d RebalanceDecision, currentVotes []VoteEntry) string {
	short := txHash
	if len(short) > 16 {
		short = short[:16]
	}
	return "[投票改投] tx=" + short +
		" yield " + formatFloat(d.CurrentYield) + "→" + formatFloat(d.TargetYield) +
		" TRX/yr (+" + formatFloat(d.Improvement) + "%)" +
		" SR " + topSRPrefix(currentVotes) + "→" + topSRPrefix(d.Target)
}

// topSRPrefix 取投票分布中票数最高的 SR 地址前 8 字符（无投票返回 "无"）。
func topSRPrefix(votes []VoteEntry) string {
	if len(votes) == 0 {
		return "无"
	}
	top := votes[0]
	for _, v := range votes[1:] {
		if v.Votes > top.Votes {
			top = v
		}
	}
	addr := top.SRAddress
	if len(addr) > 8 {
		addr = addr[:8]
	}
	return addr
}

// formatClaimAlert 格式化领奖告警（设计 §8）。
func formatClaimAlert(txHash string, amount int64) string {
	short := txHash
	if len(short) > 16 {
		short = short[:16]
	}
	return "[投票领奖] tx=" + short + " amount=" + formatFloat(float64(amount)) + " TRX"
}

// formatFloat 格式化浮点数（4 位小数，D-293 S4：原 2 位→4 位精确化）。
func formatFloat(f float64) string {
	return fmt.Sprintf("%.4f", f)
}
