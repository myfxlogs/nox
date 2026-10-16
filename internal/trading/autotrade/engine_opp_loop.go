// engine_opp_loop.go —— 存量超标拆分（棘轮消化）：oppLoop 订阅环从
// engine_opp.go 纯搬移，零行为变。
package autotrade

import (
	"context"
	"log"

	"nox/internal/infra/dephealth"
)

// oppLoop 订阅机会事件 → 过滤 → 产 intent 走 gate（D-072：风控由 gate 统一）。
// 每 loop 一个长生命周期 accumulator——当日累计、轮末 flush 覆盖写；
// 日切由 accumulator 内部 pending 队列处理（零定时器，无共享竞态）。
func (e *Engine) oppLoop(ctx context.Context) {
	ch, unsub := e.oppTopic.Subscribe()
	defer unsub()
	acc := newComboAccumulator()
	for {
		select {
		case <-ctx.Done():
			return
		case opp, ok := <-ch:
			if !ok {
				return
			}
			log.Printf("[autotrade] recv opp=%s type=%s netBps=%.2f buy=%s sell=%s", opp.ID, opp.Type, opp.NetBps, opp.VenueBuy, opp.VenueSell)
			e.handleOpportunity(ctx, opp, acc)
			// S3′-c 真心跳：每机会帧记账（闸评段 beat 键=此源）
			ferr := acc.Flush(ctx, e.store)
			dephealth.Mark("engine:opp", ferr == nil, ferr)
			if ferr != nil {
				log.Printf("[autotrade] opp rollup flush: %v", ferr)
			}
		}
	}
}
