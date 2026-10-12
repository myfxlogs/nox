// incident_exit.go —— D-013 ③：事故族 exit_kind 终态归因写点。
// 两值语义（pgstore 常量权威）：
//
//	legged_unwind  = 从未双腿成交，unwind/flatten 已成腿（LEGGED/FAILED 开仓形态）；
//	incident_close = await_session/不可行动腿 或 closed−closing>30min 且窗口内有残腿告警。
//
// 写入=pgstore.SetIncidentExitKind（覆盖策略 kind——事故归因为终态事实，非触发原因）。
package exec

import (
	"context"
	"log"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// markOpenIncident 开仓终态归因：Execute 判定 LEGGED/FAILED 时写 legged_unwind。
// FAILED 仅当 ∃ FILLED 腿才打标——全腿零成交的 FAILED 是 maker 超时撤单/
// 全腿被拒的零敞口干净终态（D-018 设计），打标会误触 D-032 事故闸全拒开（#503/#504 实证）。
// best-effort：失败记日志不阻断（exit_kind 不影响持仓安全，对账/迁移可补）；
// 腿查询失败=不可证零成交 → 保守打标（宁拦勿漏）。
func (p *Pipeline) markOpenIncident(ctx context.Context, posID int64, status string) {
	if status != "LEGGED" && status != "FAILED" {
		return
	}
	if status == "FAILED" {
		legs, err := p.store.ListPositionLegs(ctx, posID)
		if err != nil {
			log.Printf("[exec] pos %d incident legs query: %v (按事故处理)", posID, err)
		} else {
			hasFilled := false
			for _, l := range legs {
				if l.Status == "FILLED" {
					hasFilled = true
					break
				}
			}
			if !hasFilled {
				return
			}
		}
	}
	if err := p.store.SetIncidentExitKind(ctx, posID, pgstore.ExitKindLeggedUnwind); err != nil {
		log.Printf("[exec] pos %d incident exit_kind: %v", posID, err)
	}
}

// markCloseIncident 平仓终态归因（CLOSED 后调用）：
//  1. 任一 OPEN 腿 FAILED → legged_unwind（LEGGED flatten 残腿仓）；
//  2. note 含 await_session:/不可行动 token → incident_close；
//  3. closing→closed >30min 且窗口内有 critical 持仓告警 → incident_close。
//
// best-effort 同上。
func (p *Pipeline) markCloseIncident(ctx context.Context, posID int64) {
	legs, err := p.store.ListPositionLegs(ctx, posID)
	if err != nil {
		log.Printf("[exec] pos %d incident classify legs: %v", posID, err)
		return
	}
	for _, l := range legs {
		if l.Phase == "OPEN" && l.Status == "FAILED" {
			if err := p.store.SetIncidentExitKind(ctx, posID, pgstore.ExitKindLeggedUnwind); err != nil {
				log.Printf("[exec] pos %d incident exit_kind: %v", posID, err)
			}
			return
		}
	}
	pos, err := p.store.GetPosition(ctx, posID)
	if err != nil || pos == nil {
		log.Printf("[exec] pos %d incident classify position: %v", posID, err)
		return
	}
	if strings.Contains(pos.Note, "await_session:") || strings.Contains(pos.Note, "不可行动") {
		if err := p.store.SetIncidentExitKind(ctx, posID, pgstore.ExitKindIncidentClose); err != nil {
			log.Printf("[exec] pos %d incident exit_kind: %v", posID, err)
		}
		return
	}
	if pos.ClosingStartedAt != nil && pos.ClosedAt != nil &&
		pos.ClosedAt.Sub(*pos.ClosingStartedAt) > 30*time.Minute {
		has, err := p.store.HasCriticalAlertInWindow(ctx, posID, *pos.ClosingStartedAt, *pos.ClosedAt)
		if err != nil {
			log.Printf("[exec] pos %d incident classify alert: %v", posID, err)
			return
		}
		if has {
			if err := p.store.SetIncidentExitKind(ctx, posID, pgstore.ExitKindIncidentClose); err != nil {
				log.Printf("[exec] pos %d incident exit_kind: %v", posID, err)
			}
		}
	}
}
