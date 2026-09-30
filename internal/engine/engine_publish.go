package engine

import (
	"context"
	"log"
	"math"
	"time"

	"arb/internal/store/pgstore"
)

// engine_publish.go —— D-004 P1#8：发布/落库去重（每帧内存评估的产物不再每帧
// Upsert+Publish——下游 autotrade oppLoop 每事件走 DB 门禁，前端实时流会被灌满）。
//
// 判定：首见 || |netBps 变化| ≥ pubDeltaBps || 距上次发布 ≥ pubHeartbeat（=OpportunityTTL/3，
// 语义=expires_at 续命，非判定节拍）。lastPub 由 e.mu 保护（onQuote 单协程驱动，
// 锁沿用 lastScan 先例）。

// pubState oppID 上次发布态。
type pubState struct {
	netBps float64
	at     time.Time
}

// maybePublish 实质变化+心跳去重后 Upsert + oppTopic.Publish。
// lastPub 在 upsert 前先行记录：DB 故障时同一 opp 每帧重试 upsert 会放大写风暴——
// 心跳窗内的重试由下一心跳驱动，风险收敛到 pubHeartbeat 周期一次。
func (e *Engine) maybePublish(ctx context.Context, o *pgstore.Opportunity) {
	e.mu.Lock()
	last, seen := e.lastPub[o.ID]
	now := time.Now()
	publish := !seen ||
		math.Abs(o.NetBps-last.netBps) >= e.pubDeltaBps ||
		now.Sub(last.at) >= e.pubHeartbeat
	if publish {
		e.lastPub[o.ID] = pubState{netBps: o.NetBps, at: now}
	}
	e.mu.Unlock()
	if !publish {
		return
	}
	if _, err := e.store.UpsertOpportunity(ctx, o); err != nil {
		log.Printf("[engine] upsert opportunity %s: %v", o.ID, err)
		return
	}
	if e.oppTopic != nil {
		e.oppTopic.Publish(*o)
	}
}

// prunePub 清 lastPub 中 at 早于 2×OpportunityTTL 的条目（onQuote 每帧调用——
// O(n) 小表可接受；过期=该机会已从 DB TTL 过期，删除等价于「下次再见当首见」）。
func (e *Engine) prunePub() {
	ttl := e.evalCfg.OpportunityTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	cut := time.Now().Add(-2 * ttl)
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, st := range e.lastPub {
		if st.at.Before(cut) {
			delete(e.lastPub, id)
		}
	}
}
