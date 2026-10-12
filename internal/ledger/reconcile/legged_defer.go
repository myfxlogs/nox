// legged_defer.go —— D-175：混合腿 LEGGED 裸露告警的惰性复查。
// finalize 侧只登记（pgstore NOX_LEGGED_DEFERRALS）不即时 critical——独立平仓链
// （autoClose/ResumeClose）实测秒级自愈，即时告警与事实错配。本件在 Run 尾部复查：
// CLOSED→降级 info「已自动处置（耗时 Xs）」；逾期（leggedDeferWindow）仍 LEGGED/FAILED
// →critical「自动处置未闭环——需人工介入」；其余态续挂。
package reconcile

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/store/pgstore"
)

// leggedDeferWindow 延迟判定窗（票面钦定 5min 起；包级 var 供测试缩短）。
var leggedDeferWindow = 5 * time.Minute

// leggedDeferralStore 延迟登记读写面（reconcile.Store 之外的可选接口——
// store 不实现则该功能整体跳过，fakes 零迁移成本）。
type leggedDeferralStore interface {
	ListLeggedDeferrals(ctx context.Context) ([]pgstore.LeggedDeferral, error)
	ClearLeggedDeferral(ctx context.Context, posID int64) error
	GetPosition(ctx context.Context, id int64) (*pgstore.Position, error)
	InsertPositionAlert(ctx context.Context, posID int64, level, msg string) error
}

// evalLeggedDeferrals Run 尾部惰性复查挂点：每条登记按 position 现态分流。
func (r *Reconciler) evalLeggedDeferrals(ctx context.Context) {
	ds, ok := r.store.(leggedDeferralStore)
	if !ok {
		return
	}
	listCtx, cancel := r.withTimeout(ctx)
	pending, err := ds.ListLeggedDeferrals(listCtx)
	cancel()
	if err != nil {
		log.Printf("[reconcile] legged deferrals list: %v", err)
		return
	}
	now := r.now()
	for _, d := range pending {
		r.evalLeggedDeferral(ctx, ds, d, now)
	}
}

func (r *Reconciler) evalLeggedDeferral(ctx context.Context, ds leggedDeferralStore, d pgstore.LeggedDeferral, now time.Time) {
	getCtx, cancel := r.withTimeout(ctx)
	pos, err := ds.GetPosition(getCtx, d.PosID)
	cancel()
	if err != nil {
		log.Printf("[reconcile] legged deferral pos %d get: %v", d.PosID, err)
		return // fail-closed：查不到不动登记，下轮再判
	}
	switch pos.Status {
	case "CLOSED":
		elapsed := now.Sub(d.Since)
		if pos.ClosedAt != nil && pos.ClosedAt.After(d.Since) {
			elapsed = pos.ClosedAt.Sub(d.Since)
		}
		r.deferralAlert(ctx, ds, d.PosID, "info",
			fmt.Sprintf("持仓 #%d 单腿成交裸露：已自动处置（耗时 %.0fs）", d.PosID, elapsed.Seconds()))
	case "LEGGED", "FAILED":
		if now.Sub(d.Since) < leggedDeferWindow {
			return // 窗内续挂——独立平仓链仍在自愈径上
		}
		r.deferralAlert(ctx, ds, d.PosID, "critical",
			fmt.Sprintf("持仓 #%d 单腿成交裸露：自动处置未闭环 %d 分钟——需人工介入", d.PosID, int(now.Sub(d.Since).Minutes())))
	default:
		return // OPENING/OPEN/CLOSING 等过渡态续挂，由 nextBoundary 到点复查
	}
	if err := ds.ClearLeggedDeferral(ctx, d.PosID); err != nil {
		log.Printf("[reconcile] legged deferral pos %d clear: %v", d.PosID, err)
	}
}

func (r *Reconciler) deferralAlert(ctx context.Context, ds leggedDeferralStore, posID int64, level, msg string) {
	alertCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	if err := ds.InsertPositionAlert(alertCtx, posID, level, msg); err != nil {
		log.Printf("[reconcile] legged deferral pos %d alert: %v", posID, err)
	}
}

// leggedDeferralBoundary 最早未逾期→到期时刻（供 nextBoundary 挂醒）。
func (r *Reconciler) leggedDeferralBoundary(ctx context.Context) (time.Time, bool) {
	ds, ok := r.store.(leggedDeferralStore)
	if !ok {
		return time.Time{}, false
	}
	listCtx, cancel := r.withTimeout(ctx)
	pending, err := ds.ListLeggedDeferrals(listCtx)
	cancel()
	if err != nil || len(pending) == 0 {
		return time.Time{}, false
	}
	next := pending[0].Since
	for _, d := range pending[1:] {
		if d.Since.Before(next) {
			next = d.Since
		}
	}
	return next.Add(leggedDeferWindow), true
}
