package autotrade

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"arb/internal/errclass"
	"arb/internal/store/pgstore"
)

// classifyFailure 按双腿 venue 分类执行失败（D-077 修复⑩ G3）。
// 取两腿中更严重的 Action（Halt > RetryFresh > Retry > Abort）。
func (g *Gate) classifyFailure(intent TradeIntent, msg string) errclass.Action {
	a1 := errclass.ClassifyErr(intent.VenueBuy, errors.New(msg))
	a2 := errclass.ClassifyErr(intent.VenueSell, errors.New(msg))
	return worseAction(a1, a2)
}

// worseAction 返回更严重的 Action（Halt > RetryFresh > Retry > Abort）。
func worseAction(a, b errclass.Action) errclass.Action {
	severity := map[errclass.Action]int{
		errclass.Abort:      0,
		errclass.Retry:      1,
		errclass.RetryFresh: 2,
		errclass.Halt:       3,
	}
	if severity[a] >= severity[b] {
		return a
	}
	return b
}

// haltStrategy Halt 类失败 → DisableStrategy + critical 告警（D-077 修复⑩ G3）。
func (g *Gate) haltStrategy(ctx context.Context, intent TradeIntent, reason string) {
	if err := g.store.DisableStrategy(ctx, intent.Strategy, reason); err != nil {
		log.Printf("[autotrade] halt DisableStrategy %s: %v", intent.Strategy, err)
	}
	alertMsg := fmt.Sprintf("strategy %s HALTED: %s", intent.Strategy, reason)
	_ = g.store.InsertAlert(ctx, nil, "critical", alertMsg)
	log.Printf("[autotrade] HALT %s: %s", intent.Strategy, reason)
}

// rejectLog 拒绝路径统一落库 + 日志（fail-closed 检查失败与规则拒绝共用）。
// D-154 S3：disabled=true 时 stdout 1h 去重（per-strategy），DB 落库保留全量（审计完整性不动）。
func (g *Gate) rejectLog(ctx context.Context, intent TradeIntent, reason string, disabled bool) {
	_ = g.store.InsertAutotradeLog(ctx, &pgstore.AutotradeLog{
		OpportunityID: intent.DedupKey,
		Strategy:      intent.Strategy,
		Canonical:     intent.Canonical,
		VenueBuy:      intent.VenueBuy,
		VenueSell:     intent.VenueSell,
		Qty:           intent.Qty,
		Status:        "REJECTED",
		Reason:        reason,
		NetBps:        intent.NetBps,
	})
	if disabled {
		// stdout 1h 去重（per-strategy）；DB 已全量落库
		g.lastDisabledLogMu.Lock()
		last, ok := g.lastDisabledLog[intent.Strategy]
		if ok && time.Since(last) < time.Hour {
			g.lastDisabledLogMu.Unlock()
			return
		}
		g.lastDisabledLog[intent.Strategy] = time.Now()
		g.lastDisabledLogMu.Unlock()
	}
	log.Printf("[autotrade] gate REJECT %s: %s", intent.DedupKey, reason)
}

// grossSplitFloor D-179 S4：折分 qty 向下规整到 0.01 步长（FX min lot）。
// 返回规整后 qty + 是否拒开（<0.01）。宁可不开不可超限。
// D-182 S2：+1e-9 浮点容差——0.29/0.01=2.9999…96 会保守缩到 0.28（方向安全但丢精度）。
func grossSplitFloor(qty float64) (float64, bool) {
	floored := math.Floor(qty/0.01+1e-9) * 0.01
	if floored < 0.01 {
		return 0, true
	}
	return floored, false
}
