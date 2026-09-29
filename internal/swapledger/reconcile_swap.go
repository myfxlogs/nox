// D-110 P2：swap 对账告警——日频 ticker，swapledger 公式值 vs MT5 实际增量偏差>阈值告警。
//
// 时序（A2+D1 修订）：initialDelay=2h（晚于 swapledger 90s，确保当日已记账）；
// 对账前 per-leg 检查当日 cashflows 是否已写（HasCashflowForLegDay）——该腿未写跳过（不误告警）；
// 今日末条快照 ts <= 昨日末条 → 今日 MT5 未结算→跳过（D1）。
package swapledger

import (
	"arb/internal/keydomain"
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"arb/internal/safego"
	"arb/internal/store/pgstore"
)

// SwapReconcileStore 对账依赖接口（pgstore.Store 实现）。
type SwapReconcileStore interface {
	ListOpenPositionsByType(ctx context.Context, types []string) ([]pgstore.Position, error)
	ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error)
	// D-159 W3a R5：per-leg 幂等检查（pgstore HasCashflowForDay 已被 HasCashflowForLegDay 替代）。
	HasCashflowForLegDay(ctx context.Context, posID int64, legNo int16, kind string, day time.Time) (bool, error)
	SumCashflowsForDay(ctx context.Context, posID int64, kind, venue string, day time.Time) (float64, error)
	// D-349 S1：CEX perp 腿实际值=income_events 当日日和（账户级口径），rows 供结算窗语义。
	SumIncomeForDay(ctx context.Context, venue, symbol string, day time.Time) (float64, int, error)
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

	safego.Go("reconcile_swap:initial", func() {
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
	})

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
		// per-leg 预检查：该腿比对日未记 → 跳过该腿（swapledger 未记，不误告警）
		// D-314：kind per-leg 判定（XASSET_CARRY perp 腿=funding、MT 腿=swap）。
		// D-437 S1：perp 腿比对日=昨日（income 多窗结算需完整日对平，日中部分窗对全日公式必误报）；
		// MT 腿=当日不变（快照差天然含时长）。昨日无 cashflow（今日新开仓）→ 自然 skip 零特例。
		cmpDay := day
		if keydomain.IsType(leg.Venue, "perp") {
			cmpDay = day.AddDate(0, 0, -1)
		}
		kind := cashflowKind(pos.Type, leg.Venue)
		has, err := r.store.HasCashflowForLegDay(ctx, pos.ID, leg.LegNo, kind, cmpDay)
		if err != nil {
			return 0, fmt.Errorf("check cashflow: %w", err)
		}
		if !has {
			continue
		}
		alerted, err := r.reconcileLegWithStore(ctx, pos, leg.Venue, leg.Symbol, cmpDay, day)
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

// reconcileLegWithStore 取公式值+实际值后比对告警。
// D-314：posType 由 pos 取供 per-leg kind 判定（XASSET_CARRY perp 腿公式值查 kind=funding）。
// D-349 S1 腿型分流：CEX perp 腿（venue 带 ":perp" 后缀）实际值=income_events 日和——
// binance positionRisk.Order.Swap 恒 0（funding 走 income API 独立结算不入持仓 swap 字段，
// 快照差结构性恒 0=100% 假报实证 pos492）；MT 腿维持快照差路径不动。
// D-437：day=比对日（perp 调用方传昨日 cmpDay）；公式值按持仓时长折算（pos.OpenedAt
// nil→heldFrac=1 legacy 安全）——同日开仓首日全日投影 vs 部分窗实收同根误报一并修。
// now 独立入参供测试注入（生产调用方=time.Now()）。
func (r *SwapReconciler) reconcileLegWithStore(ctx context.Context, pos pgstore.Position, venue, symbol string, day, now time.Time) (bool, error) {
	kind := cashflowKind(pos.Type, venue)
	formulaUSD, err := r.store.SumCashflowsForDay(ctx, pos.ID, kind, venue, day)
	if err != nil {
		return false, err
	}

	if keydomain.IsType(venue, "perp") {
		// CEX perp 腿：实际值=income 当日实收日和（venue 剥 :perp 后缀=income_events.venue 口径）。
		actualUSD, rows, err := r.store.SumIncomeForDay(ctx, venueFromKey(venue), symbol, day)
		if err != nil {
			return false, err
		}
		// D-349 S2：0 行且公式值>0——未过结算窗 skip（0 行≠实收 0：无法区分未结算与真 0）；
		// 过窗后仍 0 实收 → 落到下方比对=真告警（funding 实收为 0 是真信息）。
		if rows == 0 && formulaUSD > 0 && !cexSettleWindowPassed(day, now) {
			return false, nil
		}
		// D-437 S2：持仓时长折算——公式值是全日投影，比对前折到持仓占比
		// （OpenedAt 在比对日内开仓 → heldFrac=(dayEnd−openedAt)/86400）。
		expectUSD := formulaUSD * heldFracOfDay(pos.OpenedAt, day)
		deviation := math.Abs(expectUSD - actualUSD)
		denom := math.Max(math.Max(math.Abs(expectUSD), math.Abs(actualUSD)), 0.01)
		deviationPct := deviation / denom * 100
		level := alertLevel(deviationPct, r.deviationPct)
		if level == "" {
			return false, nil
		}
		// D-437 S3：告警带比对日（cmpDay）自解释，防值班误判对的是哪天。
		msg := fmt.Sprintf("[息差对账] 持仓 #%d（%s %s）：公式值 $%.4f 实际收入 $%.4f，偏差 %.1f%%（阈值 %.1f%%）",
			pos.ID, venue, day.Format("2006-01-02"), expectUSD, actualUSD, deviationPct, r.deviationPct)
		if err := r.store.InsertAlert(ctx, nil, level, msg); err != nil {
			return false, fmt.Errorf("insert alert: %w", err)
		}
		log.Printf("[swap-reconcile] ALERT %s: %s", level, msg)
		return true, nil
	}

	todaySnap, err := r.store.LatestSwapSnapshot(ctx, pos.ID, venue)
	if err != nil {
		return false, nil // 无快照 → 跳过（fail-closed）
	}

	dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	yesterdaySnap, err := r.store.LatestSwapSnapshotBefore(ctx, pos.ID, venue, dayStart)
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

	msg := fmt.Sprintf("[息差对账] 持仓 #%d（%s %s）：公式值 $%.4f 实际增量 $%.4f，偏差 %.1f%%（阈值 %.1f%%）",
		pos.ID, venue, day.Format("2006-01-02"), formulaUSD, todaySnap.SwapUSD-yesterdaySnap.SwapUSD, deviationPct, r.deviationPct)
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

// heldFracOfDay D-437 S2：持仓在比对日内的时间占比 [0,1]。
// openedAt nil → 1（legacy 安全）；openedAt 在日内 → (dayEnd−openedAt)/86400；
// 日内开仓早于 dayStart（前日及更早持仓）→ clamp 至 1。
func heldFracOfDay(openedAt *time.Time, day time.Time) float64 {
	if openedAt == nil {
		return 1.0
	}
	dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.AddDate(0, 0, 1)
	start := dayStart
	if openedAt.After(dayStart) {
		start = *openedAt
	}
	frac := dayEnd.Sub(start).Seconds() / 86400
	return math.Max(0, math.Min(1, frac))
}

// cexSettleWindowPassed D-349 S2 简化选择（提示词授权）：当日 23:59:59 UTC + 5min 容差。
// binance funding 日内多期结算均落当日内（XAUUSDT 实证 4h 一结），日终窗未过前 income
// 0 行无法区分「未结算」与「实收 0」→ skip；过窗后才比对。
func cexSettleWindowPassed(day, now time.Time) bool {
	dayEnd := time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, time.UTC)
	return now.After(dayEnd.Add(5 * time.Minute))
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
