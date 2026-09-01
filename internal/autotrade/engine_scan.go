// D-182 S3 拆分：从 engine.go 提取 scanAndClose（持仓扫描+自动平仓+结算）。
package autotrade

import (
	"context"
	"log"

	"arb/internal/settlement"
)

// scanAndClose 扫描 OPEN 持仓，按 type 分派退出条件（D-072 §4.2）。
func (e *Engine) scanAndClose(ctx context.Context) {
	positions, err := e.store.ListPositions(ctx, "OPEN", 50, 0)
	if err != nil {
		log.Printf("[autotrade] list open positions: %v", err)
		return
	}
	for _, p := range positions {
		// D-182 S1：零结算检测——10min 兜底周期对活跃仓检测恒零 → settled=0。
		// 在 checkExitCondition 前执行：先写 settled=0，再判退出（settled=0 不触发误杀平仓）。
		if e.zeroDetector != nil {
			e.zeroDetector.RecordZeroForPosition(ctx, p.ID)
		}
		shouldClose, reason := e.checkExitCondition(ctx, &p)
		if !shouldClose {
			continue
		}
		log.Printf("[autotrade] auto-close pos=%d type=%s: %s", p.ID, p.Type, reason)
		if err := e.service.AutoClosePosition(ctx, p.ID); err != nil {
			log.Printf("[autotrade] auto-close pos=%d failed: %v", p.ID, err)
			continue
		}
		// D-087 §3.2：settlement.Settle 替换 updateStrategyStat（旧对象 no-op 缺陷根除）
		// D-159 W2a R3-9：settlement 失败 → 标记 settle_failed 到 position.note（可重试状态）。
		if e.settleDeps != nil {
			if err := settlement.Settle(ctx, e.settleDeps, p.ID, e.eliminator); err != nil {
				log.Printf("[autotrade] settle pos=%d: %v", p.ID, err)
				_ = e.service.Store().SetPositionNote(ctx, p.ID, "settle_failed: "+err.Error())
			}
		}
	}
}
