// reject_breaker.go —— D-560 S3：拒单签名熔断器（业主定稿混合方案）。
// 键=dedupKey|拒类；同签名连续失败 ≥3 → 熔断窗（variant 5min/liq 30min/other 30min，
// funding=暂停待确认+2h 兜底自恢复）。窗内只跳执行（闸检查照跑 fail-closed 不破）、
// 每窗只写 1 行 autotrade_log（PAUSED: <class> ×N, resume HH:MM）——审计不断洪峰归零。
// 窗口到→放行一次探测：成功=复位清态；再败=重新计时。内存 map（marginWarnThrottled
// 同款生命周期），重启重置可接受。
package autotrade

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/errclass"
	"arb/internal/store/pgstore"
)

const (
	rejectTripCount = 3 // 同签名连续失败熔断阈值
	rejectWinShort  = 5 * time.Minute
	rejectWinLong   = 30 * time.Minute
	rejectWinFund   = 2 * time.Hour // funding 暂停 2h 兜底自恢复（防业主处理完忘点钮）
)

// rejectEntry 熔断条目（key=dedupKey|class）。
type rejectEntry struct {
	errclass.RejectBreakerInfo
	consec int  // 同签名连续失败计数
	armed  bool // 熔断窗激活中
	logged bool // 本窗 PAUSED 行已写
}

func rejectWindow(class string) time.Duration {
	switch class {
	case errclass.RejectVariant:
		return rejectWinShort
	case errclass.RejectFunding:
		return rejectWinFund
	default: // liq-distance/other
		return rejectWinLong
	}
}

// breakerCheck 执行前查（breakerMu 护：dashboard ResumeBreaker/Snapshot 不经 g.mu）。
// 命中活动窗→计数+1 并返快照副本（调用方不持锁读字段）。
func (g *Gate) breakerCheck(dedupKey string) *rejectEntry {
	g.breakerMu.Lock()
	defer g.breakerMu.Unlock()
	now := time.Now()
	for k, e := range g.breaker {
		if !strings.HasPrefix(k, dedupKey+"|") {
			continue
		}
		if e.armed && now.Before(e.ResumeAt) {
			e.Count++ // 窗内抑制计数（PAUSED 行 ×N）
			cp := *e
			return &cp
		}
	}
	return nil
}

// breakerLogOnce 窗内首跳写 PAUSED 行（每窗 1 行；锁内置位防并发双写）。
func (g *Gate) breakerLogOnce(ctx context.Context, intent *TradeIntent, e *rejectEntry) {
	g.breakerMu.Lock()
	if e.logged {
		g.breakerMu.Unlock()
		return
	}
	key := intent.DedupKey + "|" + e.Class
	if cur := g.breaker[key]; cur != nil {
		if cur.logged {
			g.breakerMu.Unlock()
			return
		}
		cur.logged = true
	} else {
		e.logged = true
	}
	g.breakerMu.Unlock()
	reason := fmt.Sprintf("PAUSED: %s ×%d, resume %s", e.Class, e.Count, e.ResumeAt.Format("15:04"))
	auditWarn(ctx, g.store, "InsertAutotradeLog breaker", g.store.InsertAutotradeLog(ctx, &pgstore.AutotradeLog{
		OpportunityID: intent.DedupKey, Strategy: intent.Strategy, Canonical: intent.Canonical,
		VenueBuy: intent.VenueBuy, VenueSell: intent.VenueSell, Qty: intent.Qty,
		Status: "PAUSED", Reason: reason, NetBps: intent.NetBps,
	}))
}

// breakerNote FAILED 后记账：同签名连续+1（异签名复位其它类 consec——「同一签名连续」
// 严格语义）；≥3 起窗；探测失败（armed 且窗到后再败）→ 重新计时开新窗。
func (g *Gate) breakerNote(dedupKey, msg string) {
	class := errclass.ClassifyReject(msg)
	key := dedupKey + "|" + class
	now := time.Now()
	g.breakerMu.Lock()
	defer g.breakerMu.Unlock()
	if g.breaker == nil {
		g.breaker = map[string]*rejectEntry{}
	}
	for k, e := range g.breaker {
		if strings.HasPrefix(k, dedupKey+"|") && k != key {
			e.consec = 0 // 异签名打断连续
		}
	}
	e := g.breaker[key]
	if e == nil {
		e = &rejectEntry{RejectBreakerInfo: errclass.RejectBreakerInfo{Class: class}}
		g.breaker[key] = e
	}
	if e.armed { // 探测败：新窗（计数归一/PAUSED 行重写一次）
		e.armed, e.logged = true, false
		e.Count = 0
		e.Paused = class == errclass.RejectFunding
		e.ResumeAt = now.Add(rejectWindow(class))
		return
	}
	e.consec++
	if e.consec >= rejectTripCount {
		e.armed = true
		e.Paused = class == errclass.RejectFunding
		e.ResumeAt = now.Add(rejectWindow(class))
	}
}

// breakerPass 执行成功：清该 dedupKey 全部熔断态。
func (g *Gate) breakerPass(dedupKey string) {
	g.breakerMu.Lock()
	defer g.breakerMu.Unlock()
	for k := range g.breaker {
		if strings.HasPrefix(k, dedupKey+"|") {
			delete(g.breaker, k)
		}
	}
}

// BreakerSnapshot 透出只读快照（dashboard pending/combos 行三字段源）。
// key=dedupKey|class（调用方按 dedupKey 前缀匹配组合）。
func (g *Gate) BreakerSnapshot() map[string]errclass.RejectBreakerInfo {
	g.breakerMu.Lock()
	defer g.breakerMu.Unlock()
	out := make(map[string]errclass.RejectBreakerInfo, len(g.breaker))
	for k, e := range g.breaker {
		if e.armed { // 只透出活动窗（含已过点待探测项——resume_at 到点前端可显「即将重试」）
			out[k] = e.RejectBreakerInfo
		}
	}
	return out
}

// ResumeBreaker ResumeXAssetCombo 行为=清该 dedupKey 全部熔断态（业主「我已处理」
// → 立即放行下一探测）。返回清除条数。
func (g *Gate) ResumeBreaker(dedupKey string) int {
	g.breakerMu.Lock()
	defer g.breakerMu.Unlock()
	n := 0
	for k := range g.breaker {
		if strings.HasPrefix(k, dedupKey+"|") {
			delete(g.breaker, k)
			n++
		}
	}
	return n
}
