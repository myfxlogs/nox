// D-182 S3 拆分：从 engine.go 提取 scanAndClose（持仓扫描+自动平仓+结算）。
package autotrade

import (
	"context"
	"fmt"
	"log"

	"arb/internal/exec"
	"arb/internal/settlement"

	"arb/internal/store/pgstore"
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
		// D-181 S2：probe 仓到期评估——在 checkExitCondition 前执行（probe 拒绝优先于止损）。
		probeRes := e.evaluateProbe(ctx, &p)
		if probeRes.upgraded {
			_ = e.store.SetPositionNote(ctx, p.ID, "") // 升级常规仓，清 PROBE note
			continue                                   // 升级后本轮跳过止损检查（下一轮走常规路径）
		}
		// D-495 S3：双腿异时市可平性闸——任一 OPEN/FILLED 腿无可交易行情（休市/断流）
		// → 本轮整体 suspend：不走 probe/exit 平仓（两路径共用此闸）、不 ClaimClose 翻
		// CLOSING、不动 fundingBreach 去抖计数（pos502 周六单腿事故根因=时机盲）。
		if ok, why := e.allLegsActionable(ctx, p.ID); !ok {
			e.markLegSuspend(ctx, p.ID, why)
			continue
		} else {
			e.clearLegSuspend(p.ID)
		}
		if probeRes.shouldClose {
			log.Printf("[autotrade] auto-close pos=%d type=%s: %s", p.ID, p.Type, probeRes.reason)
			logCloseTrigger(ctx, e.store, &p, "probe: "+probeRes.reason) // D-245 触发源持久化
			if err := e.service.AutoClosePosition(ctx, p.ID); err != nil {
				log.Printf("[autotrade] auto-close pos=%d failed: %v", p.ID, err)
				continue
			}
			if e.settleDeps != nil {
				if err := settlement.Settle(ctx, e.settleDeps, p.ID, e.eliminator, e.familyResolver); err != nil {
					log.Printf("[autotrade] settle pos=%d: %v", p.ID, err)
					_ = e.service.Store().SetPositionNote(ctx, p.ID, "settle_failed: "+err.Error())
				}
			}
			continue
		}
		shouldClose, reason := e.checkExitCondition(ctx, &p)
		if !shouldClose {
			continue
		}
		log.Printf("[autotrade] auto-close pos=%d type=%s: %s", p.ID, p.Type, reason)
		logCloseTrigger(ctx, e.store, &p, "exit: "+reason) // D-245 触发源持久化
		if err := e.service.AutoClosePosition(ctx, p.ID); err != nil {
			log.Printf("[autotrade] auto-close pos=%d failed: %v", p.ID, err)
			continue
		}
		// D-087 §3.2：settlement.Settle 替换 updateStrategyStat（旧对象 no-op 缺陷根除）
		// D-159 W2a R3-9：settlement 失败 → 标记 settle_failed 到 position.note（可重试状态）。
		if e.settleDeps != nil {
			if err := settlement.Settle(ctx, e.settleDeps, p.ID, e.eliminator, e.familyResolver); err != nil {
				log.Printf("[autotrade] settle pos=%d: %v", p.ID, err)
				_ = e.service.Store().SetPositionNote(ctx, p.ID, "settle_failed: "+err.Error())
			}
		}
	}
}

// allLegsActionable D-495 S3：position 全部 OPEN+FILLED 腿过 LegActionable。
// leg.Venue=venueKey、leg.Symbol=venue 原生 symbol（persist 口径，D-495 SSOT §1.1）。
func (e *Engine) allLegsActionable(ctx context.Context, posID int64) (bool, string) {
	legs, err := e.store.ListPositionLegs(ctx, posID)
	if err != nil {
		return false, fmt.Sprintf("list legs: %v", err) // 缺数据不动作（fail-closed 同律）
	}
	for _, leg := range legs {
		if leg.Phase != "OPEN" || leg.Status != "FILLED" {
			continue
		}
		if ok, why := exec.LegActionable(ctx, e.store, leg.Venue, leg.Symbol); !ok {
			return false, why
		}
	}
	return true, ""
}

// markLegSuspend suspend 转场单条 warn（恢复后同型免再报；关市常态非异常，
// 区别于 fail-closed critical——§1.3 文案定稿）。
func (e *Engine) markLegSuspend(ctx context.Context, posID int64, why string) {
	e.legSuspendMu.Lock()
	if e.legSuspend == nil {
		e.legSuspend = make(map[int64]bool) // 测试直接构造 &Engine{} 兜底
	}
	first := !e.legSuspend[posID]
	e.legSuspend[posID] = true
	e.legSuspendMu.Unlock()
	log.Printf("[autotrade] pos=%d leg-actionable suspend: %s", posID, why)
	if !first {
		return
	}
	msg := fmt.Sprintf("品种 %s 当前无可交易行情（休市/断流），平仓挂起，行情恢复自动续平", why)
	if err := e.store.InsertPositionAlert(ctx, posID, "warn", msg); err != nil {
		log.Printf("[autotrade] pos %d suspend alert write: %v", posID, err)
	}
}

// clearLegSuspend 行情恢复出 suspend 态（下轮同型可再报）。
func (e *Engine) clearLegSuspend(posID int64) {
	e.legSuspendMu.Lock()
	delete(e.legSuspend, posID)
	e.legSuspendMu.Unlock()
}

// logCloseTrigger D-245 缺口2：平仓触发源持久化（481 实证 stdout 随容器重建丢失）。
// 写失败只 log 不阻断平仓（观测路径不挡执行路径）。
func logCloseTrigger(ctx context.Context, st *pgstore.Store, p *pgstore.Position, reason string) {
	// D-341 S3：周期学习样本——exit_kind 单写点（reason 前缀映射；IS NULL 幂等首因为准）。
	if err := st.UpdatePositionExitKind(ctx, p.ID, pgstore.ExitKindFromReason(reason)); err != nil {
		log.Printf("[autotrade] exit_kind %d: %v", p.ID, err)
	}
	posID := p.ID
	if err := st.InsertAutotradeLog(ctx, &pgstore.AutotradeLog{
		OpportunityID: p.OpportunityID,
		PositionID:    &posID,
		Strategy:      p.Type,
		Status:        "CLOSE_TRIGGER",
		Reason:        reason,
	}); err != nil {
		log.Printf("[autotrade] D-245 CLOSE_TRIGGER 落库失败 pos=%d: %v", p.ID, err)
	}
}
