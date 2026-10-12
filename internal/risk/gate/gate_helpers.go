package gate

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"arb/internal/platform/errclass"
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
	alertWrite(ctx, g.store, "critical", alertMsg)
	log.Printf("[autotrade] HALT %s: %s", intent.Strategy, reason)
}

// rejectLog 拒绝路径统一落库 + 日志（fail-closed 检查失败与规则拒绝共用）。
// disabled=true 时 stdout 1h 去重（per-strategy），DB 落库保留全量（审计完整性不动）。
func (g *Gate) rejectLog(ctx context.Context, intent TradeIntent, reason string, disabled bool) {
	AuditWarn(ctx, g.store, "InsertAutotradeLog rejectLog", g.store.InsertAutotradeLog(ctx, &pgstore.AutotradeLog{
		OpportunityID: intent.DedupKey,
		Strategy:      intent.Strategy,
		Canonical:     intent.Canonical,
		VenueBuy:      intent.VenueBuy,
		VenueSell:     intent.VenueSell,
		Qty:           intent.Qty,
		Status:        "REJECTED",
		Reason:        reason,
		NetBps:        intent.NetBps,
	}))
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
// +1e-9 浮点容差——0.29/0.01=2.9999…96 会保守缩到 0.28（方向安全但丢精度）。
func grossSplitFloor(qty float64) (float64, bool) {
	floored := math.Floor(qty/0.01+1e-9) * 0.01
	if floored < 0.01 {
		return 0, true
	}
	return floored, false
}

// auditWarn D-508 写路径审计契约·fail-open+声张：持仓/审计写失败 →
// log.Printf error + InsertAlert warn；告警写本身失败防递归只落 log（全载荷 stderr）。
// （自 engine_swaparb.go 纯搬移——D-004 P1#5 行数门禁搬迁；gate/reject_breaker 共用点不变）
func AuditWarn(ctx context.Context, store *pgstore.Store, what string, err error) {
	if err == nil {
		return
	}
	msg := fmt.Sprintf("[审计写] %s 落库失败：%v", what, err)
	log.Printf("[autotrade] %s", msg)
	if aerr := store.InsertAlert(ctx, nil, "warn", msg); aerr != nil {
		log.Printf("[autotrade] alert write failed: %v | original: %s", aerr, msg)
	}
}

// alertWrite 告警写本身失败只落 log（防递归——InsertAlert 不得再告警 InsertAlert）。
func alertWrite(ctx context.Context, store *pgstore.Store, level, msg string) {
	if err := store.InsertAlert(ctx, nil, level, msg); err != nil {
		log.Printf("[autotrade] alert write failed: %v | original: %s", err, msg)
	}
}

// finalizeSubmit Submit 执行结果收尾（D-053 自 gate.go 纯搬移零逻辑变更）：
// 修复⑩ G3 执行失败 errclass 分类落库（Halt→DisableStrategy+critical；Abort 计数），
// 资金拒单业主 warn（≥6h 节流）、D-560 签名熔断记账、D-508 审计 fail-open+声张、
// 终态 log+EXECUTED warn 告警。
func (g *Gate) finalizeSubmit(ctx context.Context, intent *TradeIntent, logEntry *pgstore.AutotradeLog,
	status, msg string, posID int64) GateResult {
	logEntry.PositionID = nil
	if posID > 0 {
		logEntry.PositionID = &posID
	}
	logEntry.Status = status
	logEntry.Reason = msg
	if status == "FAILED" {
		action := g.classifyFailure(*intent, msg)
		logEntry.Reason = action.String() + ": " + errclass.TruncateNote(msg, 120)
		if action == errclass.Halt {
			g.haltStrategy(ctx, *intent, logEntry.Reason)
		} else if action == errclass.Abort {
			log.Printf("[autotrade] gate Abort %s: %s", intent.DedupKey, msg)
		}
		g.marginWarnThrottled(ctx, *intent, msg) // D-472 S3：资金拒单→业主 warn（≥6h 节流）
		g.breakerNote(intent.DedupKey, msg)      // D-560 S3：签名熔断记账
	} else if status == "EXECUTED" {
		g.breakerPass(intent.DedupKey) // D-560 S3：探测成功=复位清态
	}
	// 写路径审计契约：审计落库失败 fail-open+声张（log+warn 告警）。
	AuditWarn(ctx, g.store, "InsertAutotradeLog gate result", g.store.InsertAutotradeLog(ctx, logEntry))

	if status == "FAILED" {
		log.Printf("[autotrade] gate FAILED %s: %s", intent.DedupKey, logEntry.Reason)
	} else {
		log.Printf("[autotrade] gate EXECUTED %s posID=%d", intent.DedupKey, posID)
		alertWrite(ctx, g.store, "warn",
			fmt.Sprintf("[机会执行] %s %s 净价差 %.1fbps 持仓#%d",
				intent.Strategy, intent.Canonical, intent.NetBps, posID))
	}
	return GateResult{Status: status, Reason: logEntry.Reason, PosID: posID}
}

// ControlPlane 导出读口（RiskDD30Check 等调用方取装配实例用）。
func (g *Gate) ControlPlane() *ControlPlane { return g.controlPlane }
