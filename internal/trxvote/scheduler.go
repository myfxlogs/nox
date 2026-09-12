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
	executor     *Executor
	store        Store
	address      string
	tronscanBase string
	config       RebalanceConfig
	account      AccountFetcher // 获取当前投票+总 TP
	now          func() time.Time
}

// Store 是 Scheduler 依赖的存储接口（解耦 pgstore.Store 供测试注入）。
type Store interface {
	InsertTrxVoteRebalance(ctx context.Context, rec TRXVoteRebalanceRecord) (int64, error)
	UpdateTrxVoteRebalanceResult(ctx context.Context, id int64, txHash, status string) error
	TrxVoteRebalancedToday(ctx context.Context, maxPerDay int) bool
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

// Run 启动两个独立 tick（设计 §5）：
// - 改投 tick：每 6h（boot 后等 5min 首次执行）
// - 领奖 tick：每 24h（UTC 00:30）
func (s *Scheduler) Run(ctx context.Context) {
	go s.rebalanceLoop(ctx)
	go s.claimLoop(ctx)
}

// rebalanceLoop 每 6h 执行改投决策。
func (s *Scheduler) rebalanceLoop(ctx context.Context) {
	ticker := time.NewTicker(6 * time.Hour)
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

// claimLoop 每 24h 执行领奖（UTC 00:30）。
func (s *Scheduler) claimLoop(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	s.waitUntilNextClaimSlot(ctx)
	if ctx.Err() != nil {
		return // ctx 已取消（容器重启），勿执行 doClaim 避免虚假告警
	}
	s.doClaim(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.doClaim(ctx)
		}
	}
}

// waitUntilNextClaimSlot 等到下一个 UTC 00:30（设计 §5）。
func (s *Scheduler) waitUntilNextClaimSlot(ctx context.Context) {
	now := s.now().UTC()
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 30, 0, 0, time.UTC)
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	select {
	case <-ctx.Done():
	case <-time.After(next.Sub(now)):
	}
}

// doRebalance 执行一轮改投决策（设计 §5 v1.1）。
func (s *Scheduler) doRebalance(ctx context.Context) {
	// 1. 检查今日是否已改投
	if s.store.TrxVoteRebalancedToday(ctx, s.config.MaxRebalancePerDay) {
		log.Println("[trxvote] 今日已改投，跳过")
		return
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
	s.executor.Alert("warn", formatRebalanceAlert(txHash, decision))
}

// doClaim 执行一轮领奖（设计 §5）。
// 带宽预检：领奖需要 ~250 BP，不足则跳过避免烧 TRX。
// 奖励阈值：Executor.ClaimReward 内部已过滤 <10 TRX，此处只处理带宽。
func (s *Scheduler) doClaim(ctx context.Context) {
	// 带宽预检（领奖 ~250 BP）
	ok, err := s.executor.HasBandwidth(ctx, BandwidthNeedClaimBP)
	if err != nil {
		log.Printf("[trxvote] claim bandwidth check: %v", err)
		s.executor.Alert("warn", "[投票领奖带宽查询失败] "+err.Error())
		return
	}
	if !ok {
		log.Println("[trxvote] 带宽不足，跳过领奖（避免烧 TRX）")
		s.executor.Alert("warn", "[投票领奖跳过] 带宽不足，等待恢复")
		return
	}

	txHash, amount, err := s.executor.ClaimReward(ctx)
	if err != nil {
		log.Printf("[trxvote] claim failed: %v", err)
		s.executor.Alert("critical", "[投票领奖失败] "+err.Error())
		return
	}
	if txHash == "" {
		log.Println("[trxvote] 奖励不足 10 TRX 或无奖励，跳过")
		return
	}
	s.executor.Alert("warn", formatClaimAlert(txHash, amount))
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

// formatRebalanceAlert 格式化改投告警（设计 §8）。
func formatRebalanceAlert(txHash string, d RebalanceDecision) string {
	short := txHash
	if len(short) > 16 {
		short = short[:16]
	}
	return "[投票改投] tx=" + short +
		" yield " + formatFloat(d.CurrentYield) + "→" + formatFloat(d.TargetYield) +
		" TRX/yr (+" + formatFloat(d.Improvement) + "%)"
}

// formatClaimAlert 格式化领奖告警（设计 §8）。
func formatClaimAlert(txHash string, amount int64) string {
	short := txHash
	if len(short) > 16 {
		short = short[:16]
	}
	return "[投票领奖] tx=" + short + " amount=" + formatFloat(float64(amount)) + " TRX"
}

// formatFloat 格式化浮点数（2 位小数）。
func formatFloat(f float64) string {
	return fmt.Sprintf("%.2f", f)
}
