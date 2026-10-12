// fact_emitter.go —— D-509 S1：推送型 fact 出口共享件（WS 流化 funding 等）。
// 推送帧天然高频（tick 级），而 LatestFacts 消费面沿用 5min 轮询时代的新鲜度语义——
// 直接逐帧落库会放大写放大；直接不去重又会丢掉「值未变也保活」语义。
// 策略：按 (kind,venue,symbol) 去重——值变即发；未变心跳发 ≤1 次/120s（对拍旧 5min 档）。
package collect

import (
	"context"
	"sync"
	"time"

	"arb/internal/data/fact"
)

// FactEmitHeartbeat 未变值重发心拍上限（1 次/120s）。对拍 5min REST 轮询旧档的
// LatestFacts 新鲜度语义，且显著优于旧档。
const FactEmitHeartbeat = 120 * time.Second

// FactEmitFunc WS 侧发射回调（装配=makeMetaAwareSink 包裹的 Sink）。
type FactEmitFunc func(ctx context.Context, fs []fact.Fact) error

type factEmitKey struct {
	kind, venue, symbol string
}

type factEmitState struct {
	value float64
	at    time.Time
}

// FactEmitter 包 collect.Sink 做 (kind,venue,symbol) 去重+节流。
type FactEmitter struct {
	sink Sink
	now  func() time.Time // 测试可覆写；默认 time.Now

	mu   sync.Mutex
	last map[factEmitKey]factEmitState
}

// NewFactEmitter 创建发射器（sink 通常=makeMetaAwareSink 产物，复用 meta 反哺+is_real 刷新）。
func NewFactEmitter(sink Sink) *FactEmitter {
	return &FactEmitter{sink: sink, now: time.Now, last: map[factEmitKey]factEmitState{}}
}

// Emit 去重+节流后发 sink：值变/首发/心跳到期（≥120s 未发）者通过，其余丢弃。
// sink 只收一批过滤后集合（保持 Sink 批次语义）。
func (e *FactEmitter) Emit(ctx context.Context, fs []fact.Fact) error {
	if e == nil || e.sink == nil || len(fs) == 0 {
		return nil
	}
	now := e.now()
	e.mu.Lock()
	out := fs[:0]
	for _, f := range fs {
		k := factEmitKey{f.Kind, f.Venue, f.Symbol}
		st, seen := e.last[k]
		if !seen || st.value != f.Value || now.Sub(st.at) >= FactEmitHeartbeat {
			e.last[k] = factEmitState{value: f.Value, at: now}
			out = append(out, f)
		}
	}
	e.mu.Unlock()
	if len(out) == 0 {
		return nil
	}
	return e.sink(ctx, out)
}
