// D-110 P2：swap 对账告警——日频 ticker，swapledger 公式值 vs MT5 实际增量偏差>阈值告警。
//
// 时序（A2+D1 修订）：initialDelay=2h（晚于 swapledger 90s，确保当日已记账）；
// 对账前 per-leg 检查当日 cashflows 是否已写（HasCashflowForLegDay）——该腿未写跳过（不误告警）；
// 今日末条快照 ts <= 昨日末条 → 今日 MT5 未结算→跳过（D1）。
package swapledger

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"arb/internal/store/pgstore"
)

// SwapReconcileStore 对账依赖接口（pgstore.Store 实现）。
type SwapReconcileStore interface {
	ListOpenPositionsByType(ctx context.Context, types []string) ([]pgstore.Position, error)
	ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error)
	// D-159 W3a R5：per-leg 幂等检查（pgstore HasCashflowForDay 已被 HasCashflowForLegDay 替代）。
	HasCashflowForLegDay(ctx context.Context, posID int64, legNo int16, kind string, day time.Time) (bool, error)
	SumCashflowsForDay(ctx context.Context, posID int64, kind, venue string, day time.Time) (float64, error)
	LatestSwapSnapshot(ctx context.Context, posID int64, venue string) (pgstore.SwapSnapshot, error)
	LatestSwapSnapshotBefore(ctx context.Context, posID int64, venue string, before time.Time) (pgstore.SwapSnapshot, error)
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}

// SwapReconciler swap 对账器（D-110 P2）。
type SwapReconciler struct {
	store        SwapReconcileStore
	deviationPct float64       // 偏差率阈值（%），默认 10.0
	runTimeout   time.Duration // 单次超时
	initialDelay time.Duration // 首轮延迟（=2h，晚于 swapledger 90s）
}

// NewSwapReconciler 创建 swap 对账器。
func NewSwapReconciler(store SwapReconcileStore, deviationPct float64) *SwapReconciler {
	if deviationPct <= 0 {
		deviationPct = 10.0
	}
	return &SwapReconciler{
		store:        store,
		deviationPct: deviationPct,
		runTimeout:   30 * time.Second,
		initialDelay: 2 * time.Hour, // A2 修订：晚于 swapledger 90s
	}
}

// StartLoop 日频 ticker（同 Ledger.StartLoop 模式）。
func (r *SwapReconciler) StartLoop(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.initialDelay):
		}
		if n, err := r.Run(ctx); err != nil {
			log.Printf("[swap-reconcile] initial: %v", err)
		} else if n > 0 {
			log.Printf("[swap-reconcile] initial: %d alerts", n)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := r.Run(ctx); err != nil {
				log.Printf("[swap-reconcile] run: %v", err)
			} else if n > 0 {
				log.Printf("[swap-reconcile] run: %d alerts", n)
			}
		}
	}
}

// Run 执行一轮对账（所有 OPEN 的 SWAPARB/CARRY 仓逐腿）。返回告警数。
func (r *SwapReconciler) Run(ctx context.Context) (int, error) {
	pctx, cancel := context.WithTimeout(ctx, r.runTimeout)
	defer cancel()

	positions, err := r.store.ListOpenPositionsByType(pctx, SwapPositionTypes)
	if err != nil {
		return 0, fmt.Errorf("list open positions: %w", err)
	}
	if len(positions) == 0 {
		return 0, nil
	}

	today := time.Now().UTC()
	alerts := 0
	for _, pos := range positions {
		n, err := r.reconcilePosition(pctx, pos, today)
		if err != nil {
			log.Printf("[swap-reconcile] position %d: %v", pos.ID, err)
			continue
		}
		alerts += n
	}
	return alerts, nil
}

// reconcilePosition 对单个持仓逐腿对账。
// D-159 W3a R5：原 position 级「当日未记→整笔跳过」改为 per-leg——该腿当日未记→跳过该腿
// （不误告警语义不变），已记腿继续对账；不再让单腿缺失短路整仓对账。
func (r *SwapReconciler) reconcilePosition(ctx context.Context, pos pgstore.Position, day time.Time) (int, error) {
	legs, err := r.store.ListPositionLegs(ctx, pos.ID)
	if err != nil {
		return 0, fmt.Errorf("list legs: %w", err)
	}

	alerts := 0
	for _, leg := range legs {
		if leg.Phase != "OPEN" {
			continue
		}
		// per-leg 预检查：该腿当日未记 swap → 跳过该腿（swapledger 未记，不误告警）
		has, err := r.store.HasCashflowForLegDay(ctx, pos.ID, leg.LegNo, "swap", day)
		if err != nil {
			return 0, fmt.Errorf("check cashflow: %w", err)
		}
		if !has {
			continue
		}
		alerted, err := r.reconcileLegWithStore(ctx, pos.ID, leg.Venue, day)
		if err != nil {
			log.Printf("[swap-reconcile] leg posID=%d venue=%s: %v", pos.ID, leg.Venue, err)
			continue
		}
		if alerted {
			alerts++
		}
	}
	return alerts, nil
}

// reconcileLegWithStore 取公式值+快照后调纯函数 reconcileLeg。
func (r *SwapReconciler) reconcileLegWithStore(ctx context.Context, posID int64, venue string, day time.Time) (bool, error) {
	formulaUSD, err := r.store.SumCashflowsForDay(ctx, posID, "swap", venue, day)
	if err != nil {
		return false, err
	}

	todaySnap, err := r.store.LatestSwapSnapshot(ctx, posID, venue)
	if err != nil {
		return false, nil // 无快照 → 跳过（fail-closed）
	}

	dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	yesterdaySnap, err := r.store.LatestSwapSnapshotBefore(ctx, posID, venue, dayStart)
	if err != nil {
		return false, nil // 无昨日快照 → 跳过
	}

	deviationPct, ok := reconcileLeg(formulaUSD, todaySnap, yesterdaySnap)
	if !ok {
		return false, nil
	}

	level := alertLevel(deviationPct, r.deviationPct)
	if level == "" {
		return false, nil
	}

	msg := fmt.Sprintf("[swap-reconcile] position_id=%d venue=%s: 公式值=$%.4f 实际增量=$%.4f 偏差率=%.1f%% (阈值=%.1f%%)",
		posID, venue, formulaUSD, todaySnap.SwapUSD-yesterdaySnap.SwapUSD, deviationPct, r.deviationPct)
	if err := r.store.InsertAlert(ctx, nil, level, msg); err != nil {
		return false, fmt.Errorf("insert alert: %w", err)
	}
	log.Printf("[swap-reconcile] ALERT %s: %s", level, msg)
	return true, nil
}

// reconcileLeg 纯函数：计算偏差率。
// todaySnap 或 yesterdaySnap 为零值 → ok=false（跳过）。
// todaySnap.Ts <= yesterdaySnap.Ts → ok=false（今日未结算，D1）。
// deviationPct = |formulaUSD - actualUSD| / max(|formulaUSD|, |actualUSD|, 0.01) * 100。
func reconcileLeg(formulaUSD float64, todaySnap, yesterdaySnap pgstore.SwapSnapshot) (deviationPct float64, ok bool) {
	if todaySnap.ID == 0 || yesterdaySnap.ID == 0 {
		return 0, false
	}
	if !todaySnap.Ts.After(yesterdaySnap.Ts) {
		return 0, false // 今日未结算（D1）
	}
	actualUSD := todaySnap.SwapUSD - yesterdaySnap.SwapUSD
	deviation := math.Abs(formulaUSD - actualUSD)
	denom := math.Max(math.Abs(formulaUSD), math.Abs(actualUSD))
	denom = math.Max(denom, 0.01) // 防除零
	return deviation / denom * 100, true
}

// alertLevel 纯函数：偏差率 → 告警级别。
// >50% → "critical"，>阈值 → "warn"，否则 ""。
// deviationPct == 阈值不告警（严格 >）。
func alertLevel(deviationPct, thresholdPct float64) string {
	if deviationPct > 50 {
		return "critical"
	}
	if deviationPct > thresholdPct {
		return "warn"
	}
	return ""
}
