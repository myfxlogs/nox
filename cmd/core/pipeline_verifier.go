// pipeline_verifier.go —— D-108 P1 R1：PositionVerifier 生产装配。
// 适配 []venue.Querier → exec.PositionVerifier（main 注入）。
// 解决 A2 fail-closed 后 verifier 未接线 = 全部平仓拒绝的部署阻塞项。
package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"nox/internal/data/venue"
	"nox/internal/trading/exec"
)

// pipelineVerifier 适配 []venue.Querier → exec.PositionVerifier（D-108 A2 生产接线）。
// querier 集合由 Broker 管理器动态刷新（set 在 SetQuerierHook 内调）。
type pipelineVerifier struct {
	mu sync.RWMutex
	qs map[string]venue.Querier // venueKey → querier
}

// newPipelineVerifier 创建适配器（初始空 map，set 后生效）。
func newPipelineVerifier() *pipelineVerifier {
	return &pipelineVerifier{qs: map[string]venue.Querier{}}
}

// set 整体替换 querier 集合（querier 启停时刷新，同 reconcile.SetQueriers 模式）。
func (v *pipelineVerifier) set(qs []venue.Querier) {
	m := make(map[string]venue.Querier, len(qs))
	for _, q := range qs {
		m[q.VenueKey()] = q
	}
	v.mu.Lock()
	v.qs = m
	v.mu.Unlock()
}

// QueryOpenedOrders 按 venueKey 查 querier（缺失→error）；per-call 10s 超时（D-108 S2 要求）；
// []venue.Order → []exec.OpenedOrder 映射（Ticket/Symbol/Side/Volume 四字段）。
func (v *pipelineVerifier) QueryOpenedOrders(ctx context.Context, venueKey string) ([]exec.OpenedOrder, error) {
	v.mu.RLock()
	q, ok := v.qs[venueKey]
	v.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("verifier: no querier for venue %s", venueKey)
	}
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	orders, err := q.QueryOpenedOrders(vctx)
	if err != nil {
		return nil, fmt.Errorf("verifier query %s: %w", venueKey, err)
	}
	out := make([]exec.OpenedOrder, len(orders))
	for i, o := range orders {
		out[i] = exec.OpenedOrder{
			Ticket:  o.Ticket,
			Symbol:  o.Symbol,
			Side:    o.Side,
			Volume:  o.Volume,
			PosSide: o.PosSide, // D-195-C2：CEX 聚合仓 position_side
		}
	}
	return out, nil
}

var _ exec.PositionVerifier = (*pipelineVerifier)(nil)
