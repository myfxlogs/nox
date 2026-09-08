package cnfi

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"time"

	"arb/internal/store/pgstore"
)

// pct-v1 分位与信号评估（D-227 C7 / D-230 F5；规格包 v0.2 §3.2——唯一数学定义）。
// 窗口=trailing 250 个有效业务日（按派生序列自身 as_of 计，缺口不补齐）；
// 当前观测点不入窗（与严格早于自身的窗口比较）；
// 分位=mid-rank：pct = (#{x<v} + 0.5·#{x=v}) / n × 100，1 位小数；
// sample_n 三档：≥250 ok（可告警）/ 120–249 reduced（observe-only）/<120 N（禁告警）。
const (
	pctV1Window        = 250
	pctV1MinSample     = 120
	signalKey3yPct     = "credit_spread_3y_pctile_pctv1"
	signalEvidenceV1   = "docs/cnfi/研究/cnfi-市场快照-2026-09-05-ZCode.md"
	signalEvidenceD259 = "docs/cnfi/D-259设计-CNFI-P2信号驱动筛选与预警-2026-09-08-Claude.md"
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
	if len(series) > 0 {
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
	} else {
		log.Printf("cnfi: signals: no derived data yet (backfill pending)")
	}
	// D-259 P2：基金信号评估（同链尾部调用，禁另起挂点；observe-only 总开关=alerts_enabled config）。
	// 独立于 credit-spread 派生数据——基金信号只依赖 NAV/holdings，无 derived 前置。
	if err := evaluateFundSignals(ctx, st); err != nil {
		log.Printf("cnfi: fund signal evaluate: %v（不阻断采集成功）", err)
	}
	return nil
}

func tierToState(tier string, pct float64) string {
	// observe-only 解除后的状态机占位（P2 复审前不生效）：阈值初值=分位>80
	if tier == "ok" && pct > 80 {
		return "armed"
	}
	return "observe"
}

// fundMetrics holds per-fund computed metrics for signal evaluation (D-259 §2).
type fundMetrics struct {
	ret1m *float64
	ret3m *float64
	ret1y *float64
	dd3m  *float64
}

// evaluateFundSignals D-259 P2：基金信号评估（①跑输/②层回撤/④排名变动）。
// 挂 EvaluateSignals 尾部（同链，禁另起挂点）；observe-only 总开关=alerts_enabled config（非 observeOnly 参数）。
// 幂等：observed_as_of == today → 跳过（日粒度）；rank_prev 每次更新。
func evaluateFundSignals(ctx context.Context, st *pgstore.Store) error {
	cfg, err := st.LoadCNFIFundSignalConfig(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cands, err := st.ListCNFICandidates(ctx)
	if err != nil {
		return fmt.Errorf("candidates: %w", err)
	}
	byLayer := map[string][]pgstore.CNFICandidate{}
	for _, c := range cands {
		byLayer[c.Layer] = append(byLayer[c.Layer], c)
	}
	holdings, err := st.ListCNFIHoldingsValued(ctx)
	if err != nil {
		return fmt.Errorf("holdings: %w", err)
	}
	heldByLayer := map[string][]string{}
	for _, h := range holdings {
		heldByLayer[h.Layer] = append(heldByLayer[h.Layer], h.Code)
	}
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	from1m := now.AddDate(0, -1, 0).Format("2006-01-02")
	from3m := now.AddDate(0, -3, 0).Format("2006-01-02")
	from1y := now.AddDate(-1, 0, 0).Format("2006-01-02")

	for layer, layerCands := range byLayer {
		metrics := computeFundMetrics(ctx, st, layerCands, today, from1m, from3m, from1y)
		median1m := layerMedian1m(layerCands, metrics)
		ranks := computeLayerRanks(layerCands, metrics)
		heldCodes := heldByLayer[layer]

		// ② 层组合 3m 最大回撤（持仓基金中最大单基金回撤，保守口径）
		var layerDD *float64
		for _, code := range heldCodes {
			if m := metrics[code]; m != nil && m.dd3m != nil {
				if layerDD == nil || *m.dd3m > *layerDD {
					layerDD = m.dd3m
				}
			}
		}
		if layerDD != nil && *layerDD >= cfg.LayerDDPct {
			key := fmt.Sprintf("layer_dd_%s", layer)
			if evalToday, _ := st.CNFISignalStateAsOf(ctx, key); evalToday != today {
				upsertFundSignal(ctx, st, key, *layerDD, len(heldCodes), today, cfg, "层 %s 组合 3m 回撤 %.2f%%", layer, *layerDD)
			}
		}

		for _, code := range heldCodes {
			m := metrics[code]
			if m == nil {
				continue
			}
			// ① 跑输层内中位
			if median1m != nil && m.ret1m != nil {
				underperf := (*median1m - *m.ret1m) * 100 // % → bp
				if underperf >= cfg.UnderperfBps {
					key := fmt.Sprintf("fund_underperf_%s", code)
					if evalToday, _ := st.CNFISignalStateAsOf(ctx, key); evalToday != today {
						upsertFundSignal(ctx, st, key, underperf, len(layerCands), today, cfg, "基金 %s 1m 跑输层内中位 %.0fbp", code, underperf)
					}
				}
			}
			// ④ 排名变动
			if rank, ok := ranks[code]; ok {
				prevKey := fmt.Sprintf("rank_prev_%s", code)
				if prevVal, err := st.CNFISignalStateValue(ctx, prevKey); err != nil {
					log.Printf("cnfi: fund signals: read %s: %v", prevKey, err)
				} else if prevVal != nil {
					diff := math.Abs(float64(rank) - *prevVal)
					if diff >= float64(cfg.RankShift) {
						key := fmt.Sprintf("rank_shift_%s", code)
						if evalToday, _ := st.CNFISignalStateAsOf(ctx, key); evalToday != today {
							upsertFundSignal(ctx, st, key, diff, len(layerCands), today, cfg, "基金 %s 层内排名变动 %.0f 位", code, diff)
						}
					}
				}
				// rank_prev 每次更新（非信号，前值存储）
				if err := st.UpsertCNFISignalState(ctx, prevKey, "observe", float64(rank), len(layerCands), today, signalEvidenceD259); err != nil {
					log.Printf("cnfi: fund signals: upsert %s: %v", prevKey, err)
				}
			}
		}
	}
	return nil
}

// computeFundMetrics computes 1m/3m/1y returns and 3m drawdown per candidate (D-247 §4 唯一数学定义).
func computeFundMetrics(ctx context.Context, st *pgstore.Store, cands []pgstore.CNFICandidate, today, from1m, from3m, from1y string) map[string]*fundMetrics {
	metrics := map[string]*fundMetrics{}
	for _, c := range cands {
		m := &fundMetrics{}
		metrics[c.Code] = m
		series, err := st.CNFINAVSeries(ctx, c.Code)
		if err != nil {
			log.Printf("cnfi: fund signals: nav %s: %v", c.Code, err)
			continue
		}
		pts := make([]LJJZPoint, 0, len(series))
		for _, r := range series {
			pts = append(pts, LJJZPoint{Date: r.Date, LJJZ: r.LJJZ})
		}
		s1m := SliceByDate(pts, from1m, today)
		if c.BreakDate == nil || !HasCaliberBreak(s1m, from1m, today, *c.BreakDate) {
			m.ret1m = IntervalReturn(s1m)
		}
		s3m := SliceByDate(pts, from3m, today)
		if c.BreakDate == nil || !HasCaliberBreak(s3m, from3m, today, *c.BreakDate) {
			m.ret3m = IntervalReturn(s3m)
			m.dd3m = MaxDrawdown(s3m)
		}
		s1y := SliceByDate(pts, from1y, today)
		if c.BreakDate == nil || !HasCaliberBreak(s1y, from1y, today, *c.BreakDate) {
			m.ret1y = IntervalReturn(s1y)
		}
	}
	return metrics
}

// layerMedian1m returns the median 1m return across candidates with data (D-259 §2).
func layerMedian1m(cands []pgstore.CNFICandidate, metrics map[string]*fundMetrics) *float64 {
	var vals []float64
	for _, c := range cands {
		if m := metrics[c.Code]; m != nil && m.ret1m != nil {
			vals = append(vals, *m.ret1m)
		}
	}
	if len(vals) == 0 {
		return nil
	}
	sort.Float64s(vals)
	v := vals[len(vals)/2]
	return &v
}

// computeLayerRanks returns code→rank (1-based, by 1y fallback 3m return desc; D-247 §4).
func computeLayerRanks(cands []pgstore.CNFICandidate, metrics map[string]*fundMetrics) map[string]int {
	type entry struct {
		code string
		ret  float64
	}
	var entries []entry
	for _, c := range cands {
		m := metrics[c.Code]
		if m == nil {
			continue
		}
		if m.ret1y != nil {
			entries = append(entries, entry{c.Code, *m.ret1y})
		} else if m.ret3m != nil {
			entries = append(entries, entry{c.Code, *m.ret3m})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ret > entries[j].ret })
	ranks := map[string]int{}
	for i, e := range entries {
		ranks[e.code] = i + 1
	}
	return ranks
}

// upsertFundSignal writes signal state + optional alert (alerts_enabled gate; C11 文案=事实陈述).
func upsertFundSignal(ctx context.Context, st *pgstore.Store, key string, value float64, sampleN int, today string, cfg pgstore.CNFIFundSignalConfig, msgFmt string, args ...any) {
	if err := st.UpsertCNFISignalState(ctx, key, "observe", value, sampleN, today, signalEvidenceD259); err != nil {
		log.Printf("cnfi: fund signals: upsert %s: %v", key, err)
		return
	}
	if cfg.AlertsEnabled {
		msg := fmt.Sprintf("CN-FI "+msgFmt, args...)
		if err := st.InsertAlert(ctx, nil, "warn", msg); err != nil {
			log.Printf("cnfi: fund signals: alert %s: %v", key, err)
		}
	}
}
