package exec

// carry 入口 funding regime 持久性闸（XASSET_CARRY）。
// 背景=pos 485 归因：入口只验当期 funding 符号，regime 坍塌途中照放（退出侧有「连 3 期转正」
// 平仓规则而入口无对称闸）。数据源=kind='funding' 预测流 facts 全史（funding_history 结算史
// 24h 批量不可用于入场时点判定）。规则=收入腿制（§7 P1 修正：perp×perp 配对存在，
// 仅约束收入腿、支出腿豁免），窗口内任一样本破 floor 即拒，样本不足 fail-closed。

import (
	"context"
	"fmt"
	"nox/internal/data/keydomain"
	"nox/internal/platform/rejects"
	"sync"
	"time"

	"nox/internal/store/pgstore"
)

// rejects 注册：funding_absent 面门集（D-652 拒因可观测——注册了不挂/挂了没注册由对拍锚防）。
func init() { rejects.Register("funding_absent", "absent_block") }

// FundingWindowQuerier funding 预测流窗口查询（pgstore.Store 实现，D-328 S1）。
type FundingWindowQuerier interface {
	FundingPredWindow(ctx context.Context, venue, symbol string, since time.Time) ([]pgstore.FactPoint, error)
}

// CarryRegimeConfig 持久性闸三参数（D-328 S5 env 注入；零值=fail-closed 全拒 XASSET）。
type CarryRegimeConfig struct {
	Window      time.Duration // 回看窗口（默认 24h）
	FloorPct    float64       // 收入下限（年化百分点，默认 2.0；区分「弱而活」与「已死」）
	MinSamples  int           // 窗口最少样本数（默认 12；不足=fail-closed）
	PulsedAware bool          // D-334c S1：demo 域脉动型 funding 品种均值语义（mean+latest 判）；real 恒 false=worst 严判一字不动
}

// FundingRegimeOK 单腿持久性判定（纯函数，可单测；rows 按 ts 升序）。
// 返回 gate=false=该腿拒绝；income=false=非收入腿（调用方跳过不约束）。
//   - 样本不足（含零样本）→ (false,false,"insufficient n ...")——未知 regime 不放行
//   - requiredSign×latest ≤ 0 → (true,false,"non-income leg skip ...")——当前付息/零息腿豁免
//   - 收入腿：窗口内全部样本 requiredSign×v ≥ floorPct（sign>0 看 minV、sign<0 看 maxV≤−floor）
//
// reason 串带 latest/min/max/n 供日志与 FAILED message。
func FundingRegimeOK(rows []pgstore.FactPoint, requiredSign, floorPct float64, minSamples int, pulsedAware bool) (gate bool, income bool, reason string) {
	n := len(rows)
	if n == 0 || n < minSamples {
		return false, false, fmt.Sprintf("insufficient n=%d < %d", n, minSamples)
	}
	latest := rows[n-1].Value
	if requiredSign*latest <= 0 {
		return true, false, fmt.Sprintf("non-income leg skip latest=%.4g", latest)
	}
	// demo 域脉动型分支（PulsedAware）——mean≥floor && latest≥floor && n≥minSamples
	//（n 检查已在函数头）。经济语义：均值含零样本=真实期望收益（零期拉低均值是诚实反映非一票否决）；
	// latest=当下未坍塌；fail-closed 保留。已知取舍（显式接受）：窗内中段塌落（latest 尚正、均值
	// 尚高）逃逸比 worst 判多——demo=学习环境；real 域不受影响（一字不动=铁边界）。
	if pulsedAware {
		var sum float64
		for _, r := range rows {
			sum += requiredSign * r.Value
		}
		mean := sum / float64(n)
		signedLatest := requiredSign * latest
		if mean < floorPct || signedLatest < floorPct {
			return false, true, fmt.Sprintf("mean=%.4g latest=%.4g n=%d < floor %.2f", mean, signedLatest, n, floorPct)
		}
		return true, true, fmt.Sprintf("income ok (demo pulsed) mean=%.4g latest=%.4g n=%d", mean, signedLatest, n)
	}
	minV, maxV := rows[0].Value, rows[0].Value
	for _, r := range rows[1:] {
		if r.Value < minV {
			minV = r.Value
		}
		if r.Value > maxV {
			maxV = r.Value
		}
	}
	worst := minV // requiredSign×v 的窗口最小值
	if requiredSign < 0 {
		worst = -maxV
	}
	if worst < floorPct {
		return false, true, fmt.Sprintf("latest=%.4g min=%.4g max=%.4g worst=%.4g n=%d < floor %.2f", latest, minV, maxV, worst, n, floorPct)
	}
	return true, true, fmt.Sprintf("income ok latest=%.4g min=%.4g max=%.4g n=%d", latest, minV, maxV, n)
}

// worstOf 拒绝腿 worst 值复算（自动标径用——worst=0 且样本含原播 0 才标）。
func worstOf(rows []pgstore.FactPoint, requiredSign float64) float64 {
	if len(rows) == 0 {
		return 0
	}
	minV, maxV := rows[0].Value, rows[0].Value
	for _, r := range rows[1:] {
		if r.Value < minV {
			minV = r.Value
		}
		if r.Value > maxV {
			maxV = r.Value
		}
	}
	if requiredSign < 0 {
		return -maxV
	}
	return minV
}

// CarryFundingRegimeCheck XASSET_CARRY 入口持久性闸（S3 autotrade/S4 manual 双入口共用 helper）。
// 逐 :perp 腿独立判定：收入腿过闸、非收入腿豁免（perp×perp 价差结构不误杀）；
// 查询失败 fail-closed 拒绝。ok=false 时 reason 带腿与统计供日志/FAILED message。
// 腿 symbol 口径与 D-318 fillSettleRef 同源：买侧=canonical、卖侧=canonicalSell（空回退 canonical）；
// funding fact 键=裸 venue（去 :perp 后缀）+canonical symbol。
func CarryFundingRegimeCheck(ctx context.Context, q FundingWindowQuerier, venueBuy, venueSell, canonical, canonicalSell string, now time.Time, cfg CarryRegimeConfig) (bool, string) {
	sellSym := canonicalSell
	if sellSym == "" {
		sellSym = canonical
	}
	legs := []struct {
		venueKey, symbol string
		sign             float64 // 卖侧空 perp 收正 funding=+1；买侧多 perp 收负 funding=−1
	}{
		{venueBuy, canonical, -1},
		{venueSell, sellSym, +1},
	}
	for _, leg := range legs {
		if !keydomain.IsType(leg.venueKey, "perp") {
			continue // 非 perp 腿无 funding 收益源，不约束（T5）
		}
		// funding_absent 上游拦（STATE 行50）：账本在册=venue 无 funding 面——
		// 如实拒不撞 worst 闸（原 worst=0 结构性永拒病位）。读败 fail-closed。
		if hit, herr := fundingAbsentHit(ctx, q, leg.symbol, keydomain.BareOf(leg.venueKey)); herr != nil {
			return false, fmt.Sprintf("%s/%s funding_absent 查败: %v (fail-closed)", leg.venueKey, leg.symbol, herr)
		} else if hit {
			rejects.Inc(rejects.Key{Face: "funding_absent", Gate: "absent_block"})
			return false, fmt.Sprintf("%s/%s funding_absent 在册（venue 无 funding 面）", leg.venueKey, leg.symbol)
		}
		rows, err := q.FundingPredWindow(ctx, keydomain.BareOf(leg.venueKey), leg.symbol, now.Add(-cfg.Window))
		if err != nil {
			return false, fmt.Sprintf("%s/%s query: %v (fail-closed)", leg.venueKey, leg.symbol, err)
		}
		// 非收入腿豁免语义在 FundingRegimeOK 内判定（成本侧持久性另评），本层只看总闸。
		gate, _, reason := FundingRegimeOK(rows, leg.sign, cfg.FloorPct, cfg.MinSamples, cfg.PulsedAware)
		if !gate {
			maybeMarkFundingAbsent(ctx, q, keydomain.BareOf(leg.venueKey), leg.symbol, rows, worstOf(rows, leg.sign))
			return false, fmt.Sprintf("%s/%s %s", leg.venueKey, leg.symbol, reason)
		}
	}
	return true, ""
}

// ---- funding_absent 能力账本联动（STATE 行50——venue 无 funding 面品种如实拦）----

// FundingAbsentStore 缺位账本读/写口（pgstore.Store 实现；可选接口——
// FundingWindowQuerier 不强制实现，测试替身零改）。
type FundingAbsentStore interface {
	IsFundingAbsent(ctx context.Context, canonical, venue string) (bool, error)
	UpsertCapabilityConstraint(ctx context.Context, c *pgstore.CapabilityConstraint) error
}

// fundingAbsentMarkStreak 自动标径所需连拍数（「闸连续 N 拍 worst=0 且样本含原
// 播 0」防簿腐——3 拍同 STATE 行50 连拍定性惯例）。
const fundingAbsentMarkStreak = 3

var absentMarkMu struct {
	sync.Mutex
	m map[string]int
}

// fundingAbsentHit 账本查（可选接口——querier 不实现=无标注面，原 worst 闸照旧）。
func fundingAbsentHit(ctx context.Context, q FundingWindowQuerier, canonical, venue string) (bool, error) {
	as, ok := q.(FundingAbsentStore)
	if !ok {
		return false, nil
	}
	return as.IsFundingAbsent(ctx, canonical, venue)
}

// maybeMarkFundingAbsent 自动标径：worst=0 拒 + 样本含 venue 原播 0 → 连拍计数
// ≥3 → funding_absent:<sym>@<venue> 落账（读败/写败静默下拍——标注面非判据面）。
func maybeMarkFundingAbsent(ctx context.Context, q FundingWindowQuerier, venue, symbol string, rows []pgstore.FactPoint, worst float64) {
	as, ok := q.(FundingAbsentStore)
	if !ok || worst != 0 {
		return
	}
	nativeZero := false
	for _, r := range rows {
		if r.Value == 0 {
			nativeZero = true
			break
		}
	}
	if !nativeZero {
		return
	}
	key := venue + "|" + symbol
	absentMarkMu.Lock()
	if absentMarkMu.m == nil {
		absentMarkMu.m = map[string]int{}
	}
	absentMarkMu.m[key]++
	n := absentMarkMu.m[key]
	absentMarkMu.Unlock()
	if n < fundingAbsentMarkStreak {
		return
	}
	one := 1.0
	_ = as.UpsertCapabilityConstraint(ctx, &pgstore.CapabilityConstraint{
		Scope: "global", Subject: pgstore.FundingAbsentSubject(symbol, venue),
		ValueNum: &one,
		Note:     fmt.Sprintf("worst=0×%d 拍+样本含原播0——venue 无 funding 面如实拦", n),
		Source:   "funding-absent-auto",
	})
}
