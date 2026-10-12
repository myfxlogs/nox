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
// D-004 P1#4：循环体抽为 evaluatePosition——positionLoop quote 快路命中时
// 单仓升权威评估复用同一实现（禁第二份判定）。
func (e *Engine) scanAndClose(ctx context.Context) {
	if e.scanHook != nil {
		e.scanHook()
	}
	positions, err := e.store.ListPositions(ctx, "OPEN", 50, 0)
	if err != nil {
		log.Printf("[autotrade] list open positions: %v", err)
		return
	}
	for i := range positions {
		e.evaluatePosition(ctx, &positions[i])
	}
}

// evaluatePosition 单仓权威退出评估（D-004 P1#4 抽出，逻辑一字不改）：
// zeroDetector 测量 → probe 到期评估 → 全腿可平性闸 → probe/exit 平仓 → settle。
// 返回该仓本轮是否触发平仓（closed=已发 AutoClosePosition 成功）。
func (e *Engine) evaluatePosition(ctx context.Context, p *pgstore.Position) bool {
	if e.evalHook != nil {
		e.evalHook(p.ID)
	}
	// D-182 S1：零结算检测——全量路径对活跃仓检测恒零 → settled=0。
	// 在 checkExitCondition 前执行：先写 settled=0，再判退出（settled=0 不触发误杀平仓）。
	if e.zeroDetector != nil {
		e.zeroDetector.RecordZeroForPosition(ctx, p.ID)
	}
	// D-181 S2：probe 仓到期评估——在 checkExitCondition 前执行（probe 拒绝优先于止损）。
	probeRes := e.evaluateProbe(ctx, p)
	if probeRes.upgraded {
		_ = e.store.SetPositionNote(ctx, p.ID, "") // 升级常规仓，清 PROBE note
		return false                               // 升级后本轮跳过止损检查（下一轮走常规路径）
	}
	// D-495 S3：双腿异时市可平性闸——任一 OPEN/FILLED 腿无可交易行情（休市/断流）
	// → 本轮整体 suspend：不走 probe/exit 平仓（两路径共用此闸）、不 ClaimClose 翻
	// CLOSING、不动 fundingBreach 去抖计数（pos502 周六单腿事故根因=时机盲）。
	if ok, why := e.allLegsActionable(ctx, p.ID); !ok {
		e.markLegSuspend(ctx, p.ID, why)
		return false
	} else {
		e.clearLegSuspend(p.ID)
	}
	if probeRes.shouldClose {
		log.Printf("[autotrade] auto-close pos=%d type=%s: %s", p.ID, p.Type, probeRes.reason)
		logCloseTrigger(ctx, e.store, p, "probe: "+probeRes.reason) // D-245 触发源持久化
		if err := e.service.AutoClosePosition(ctx, p.ID); err != nil {
			log.Printf("[autotrade] auto-close pos=%d failed: %v", p.ID, err)
			return false
		}
		if e.settleDeps != nil {
			if err := settlement.Settle(ctx, e.settleDeps, p.ID, e.eliminator, e.familyResolver); err != nil {
				log.Printf("[autotrade] settle pos=%d: %v", p.ID, err)
				_ = e.service.Store().SetPositionNote(ctx, p.ID, "settle_failed: "+err.Error())
			}
		}
		e.notifyPositionClosed(ctx, p, "probe: "+probeRes.reason) // D-111②
		return true
	}
	// D-053 ③ R1②：高影响财历事件禁区——非 XASSET 类型禁区内推迟本轮常规退出
	// （markLegSuspend 同转场，窗过后自恢复）；fail-open（查询失败不推迟）。
	// XASSET_CARRY 不进此分支——禁区判定下沉 xassetExitCheck（disaster 保命链恒可达）。
	if hit, why := e.macroBlackoutSuspend(ctx, p); hit {
		e.markLegSuspend(ctx, p.ID, why)
		return false
	} else {
		e.clearLegSuspend(p.ID)
	}
	shouldClose, reason := e.checkExitCondition(ctx, p)
	if !shouldClose {
		return false
	}
	log.Printf("[autotrade] auto-close pos=%d type=%s: %s", p.ID, p.Type, reason)
	logCloseTrigger(ctx, e.store, p, "exit: "+reason) // D-245 触发源持久化
	if err := e.service.AutoClosePosition(ctx, p.ID); err != nil {
		log.Printf("[autotrade] auto-close pos=%d failed: %v", p.ID, err)
		return false
	}
	// D-087 §3.2：settlement.Settle 替换 updateStrategyStat（旧对象 no-op 缺陷根除）
	// D-159 W2a R3-9：settlement 失败 → 标记 settle_failed 到 position.note（可重试状态）。
	if e.settleDeps != nil {
		if err := settlement.Settle(ctx, e.settleDeps, p.ID, e.eliminator, e.familyResolver); err != nil {
			log.Printf("[autotrade] settle pos=%d: %v", p.ID, err)
			_ = e.service.Store().SetPositionNote(ctx, p.ID, "settle_failed: "+err.Error())
		}
	}
	e.notifyPositionClosed(ctx, p, "exit: "+reason) // D-111②
	return true
}

// notifyPositionClosed D-111②：平仓成功落 warn alert（→alerts→feishu 扇出 D-508）。
// 此前正常平仓只写 autotrade_logs+stdout——开仓有 [机会执行] 推送、平仓静默的不对称修平；
// realized 取 settle 后库值（settle 失败/延迟→省略该段不阻塞通知本体）。#509 disaster 平掉
// 业主无感即本缺口实证。
func (e *Engine) notifyPositionClosed(ctx context.Context, p *pgstore.Position, reason string) {
	realized := ""
	if np, err := e.store.GetPosition(ctx, p.ID); err == nil && np.RealizedPnl != nil {
		realized = fmt.Sprintf("，realized=%.2f USD", *np.RealizedPnl)
	}
	msg := fmt.Sprintf("[自动平仓] 持仓#%d %s：%s%s", p.ID, p.Type, reason, realized)
	if err := e.store.InsertPositionAlert(ctx, p.ID, "warn", msg); err != nil {
		log.Printf("[autotrade] pos %d close notify write: %v", p.ID, err)
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
		var prober exec.TradeModeProber
		if e.tradeModeProber != nil {
			prober = e.tradeModeProber(leg.Venue)
		}
		if ok, why := exec.LegActionable(ctx, e.store, prober, leg.Venue, leg.Symbol, leg.Side, true); !ok {
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
