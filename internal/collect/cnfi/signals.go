package cnfi

import (
	"context"
	"fmt"
	"log"
	"sort"

	"arb/internal/store/pgstore"
)

// pct-v1 分位与信号评估（D-227 C7 / D-230 F5；规格包 v0.2 §3.2——唯一数学定义）。
// 窗口=trailing 250 个有效业务日（按派生序列自身 as_of 计，缺口不补齐）；
// 当前观测点不入窗（与严格早于自身的窗口比较）；
// 分位=mid-rank：pct = (#{x<v} + 0.5·#{x=v}) / n × 100，1 位小数；
// sample_n 三档：≥250 ok（可告警）/ 120–249 reduced（observe-only）/<120 N（禁告警）。
const (
	pctV1Window      = 250
	pctV1MinSample   = 120
	signalKey3yPct   = "credit_spread_3y_pctile_pctv1"
	signalEvidenceV1 = "docs/cnfi/研究/cnfi-市场快照-2026-09-05-ZCode.md"
)

// PercentilePctV1 mid-rank 经验分位（0-100，1 位小数）。window 不得含当前值。
func PercentilePctV1(window []float64, v float64) float64 {
	if len(window) == 0 {
		return 0
	}
	less, equal := 0, 0
	for _, x := range window {
		switch {
		case x < v:
			less++
		case x == v:
			equal++
		}
	}
	pct := (float64(less) + 0.5*float64(equal)) / float64(len(window)) * 100
	return float64(int(pct*10+0.5*sign(pct))) / 10 // 1 位小数（正数域四舍五入）
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// QualityTier maps sample_n to the three-tier behavior table (唯一行为表，F5).
func QualityTier(sampleN int) string {
	switch {
	case sampleN >= pctV1Window:
		return "ok"
	case sampleN >= pctV1MinSample:
		return "reduced"
	default:
		return "N"
	}
}

// EvaluateSignals computes pct-v1 percentiles for the 3y credit spread and persists
// observe-only signal state (C11: 观察期一律 observe，不触发任何告警).
// observeOnly=true 时不评估 armed/fired 转换——解除须 P2 复审后由 signal_config 驱动。
func EvaluateSignals(ctx context.Context, st *pgstore.Store, observeOnly bool) error {
	// 当前值 = 最新共同 as_of 的派生值（窗口取严格更早的 250 日）。
	series, err := st.CNFIDerivedSpread(ctx, "ig_aaa", "3y", "cgb", "3y", pctV1Window+1)
	if err != nil {
		return fmt.Errorf("cnfi signals: derived spread: %w", err)
	}
	if len(series) == 0 {
		log.Printf("cnfi: signals: no derived data yet (backfill pending)")
		return nil
	}
	sort.Slice(series, func(i, j int) bool { return series[i].AsOf > series[j].AsOf })
	current := series[0]
	var window []float64
	for _, p := range series[1:] { // 严格早于当前点
		window = append(window, p.ValueBP)
	}
	sampleN := len(window)
	pct := PercentilePctV1(window, current.ValueBP)
	state := "observe"
	if !observeOnly {
		state = tierToState(QualityTier(sampleN), pct)
	}
	if err := st.UpsertCNFISignalState(ctx, signalKey3yPct, state, pct, sampleN, current.AsOf, signalEvidenceV1); err != nil {
		return fmt.Errorf("cnfi signals: upsert state: %w", err)
	}
	log.Printf("cnfi: signal %s value=%.1f pctile=%.1f sample_n=%d tier=%s state=%s",
		signalKey3yPct, current.ValueBP, pct, sampleN, QualityTier(sampleN), state)
	return nil
}

func tierToState(tier string, pct float64) string {
	// observe-only 解除后的状态机占位（P2 复审前不生效）：阈值初值=分位>80
	if tier == "ok" && pct > 80 {
		return "armed"
	}
	return "observe"
}
