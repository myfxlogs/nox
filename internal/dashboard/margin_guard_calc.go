// margin_guard_calc.go —— D-405 S3 纯函数判定层（零 IO 可测）。
// 三档语义（SSOT §1）：预警 warn=free<3×Σ(腿名义×日波动) / buffer<3；
// 紧急 urgent=free<1.5×同上 / buffer<1.5；data_stale=数据缺（fail-open 禁保留陈旧绿）。
package dashboard

import "time"

// 档位阈值（SSOT §1 钉死值）。
const (
	marginWarnMultiple   = 3.0
	marginUrgentMultiple = 1.5
)

// marginVenueClass venue_type → 判定族：cfd（free_margin×波动缓冲）/ cex（buffer 倍数）/
// nonMargin（spot/funding/earn 无保证金概念，只入开户门槛档——SSOT §1 刻意不做 margin 档）。
func marginVenueClass(venueType string) string {
	switch venueType {
	case "mt4", "mt5":
		return "cfd"
	case "unified", "perp":
		return "cex"
	default:
		return "nonMargin"
	}
}

// MarginEvalIn 单 venue 评估输入（快照行 + 波动求和结果，nil=列未采）。
type MarginEvalIn struct {
	VenueType   string
	HasLegs     bool // 该 venue 有 OPEN 腿（空仓 venue 无保证金风险）
	Equity      *float64
	FreeMargin  *float64 // MT free_margin_raw
	Buffer      *float64 // CEX buffer_multiple
	DailyVolUSD float64  // Σ(腿名义额×日波动)；无腿=0
}

// MarginEvalOut 判定结果（coverage<0 = 不适用/不可算）。
type MarginEvalOut struct {
	Tier              string
	CoverageDays      float64 // <0 = 不适用
	AvailMarginUSD    float64 // 可用保证金头room（MT=free_margin；CEX=equity−MMR）
	SuggestedTopupUSD float64
}

// evalMarginTier 三档判定纯函数。
func evalMarginTier(in MarginEvalIn) MarginEvalOut {
	out := MarginEvalOut{CoverageDays: -1}
	switch marginVenueClass(in.VenueType) {
	case "nonMargin":
		out.Tier = "normal" // 现货/资金账户无保证金概念（SSOT §1）
		return out
	case "cfd":
		if in.Equity == nil || in.FreeMargin == nil {
			out.Tier = "data_stale"
			return out
		}
		out.AvailMarginUSD = *in.FreeMargin
		if !in.HasLegs || in.DailyVolUSD <= 0 {
			out.Tier = "normal"
			return out
		}
		out.CoverageDays = *in.FreeMargin / in.DailyVolUSD
		if topup := marginWarnMultiple*in.DailyVolUSD - *in.FreeMargin; topup > 0 {
			out.SuggestedTopupUSD = topup
		}
		switch {
		case *in.FreeMargin < marginUrgentMultiple*in.DailyVolUSD:
			out.Tier = "urgent"
		case *in.FreeMargin < marginWarnMultiple*in.DailyVolUSD:
			out.Tier = "warn"
		default:
			out.Tier = "normal"
		}
		return out
	default: // cex
		if in.Equity == nil {
			out.Tier = "data_stale"
			return out
		}
		if !in.HasLegs {
			out.Tier = "normal" // 空仓 venue 无保证金风险（MMR=0 缓冲无穷大）
			return out
		}
		if in.Buffer == nil || *in.Buffer <= 0 {
			out.Tier = "data_stale" // 有仓无 buffer=采集洞，禁陈旧绿
			return out
		}
		b := *in.Buffer
		mmr := *in.Equity / b
		out.AvailMarginUSD = *in.Equity - mmr
		if out.AvailMarginUSD < 0 {
			out.AvailMarginUSD = 0
		}
		if in.DailyVolUSD > 0 {
			out.CoverageDays = out.AvailMarginUSD / in.DailyVolUSD
		}
		// 预警档缺口=拉回 buffer≥3 所需注资（equity ≥ 3×MMR）。
		if topup := marginWarnMultiple*mmr - *in.Equity; topup > 0 {
			out.SuggestedTopupUSD = topup
		}
		switch {
		case b < marginUrgentMultiple:
			out.Tier = "urgent"
		case b < marginWarnMultiple:
			out.Tier = "warn"
		default:
			out.Tier = "normal"
		}
		return out
	}
}

// shouldMarginAlert 冷却+去抖判定（纯函数）：
// urgent 档须连续 2 tick（urgentStreak≥2）才发；冷却分档（urgent 30min/warn 6h，
// 同 venue 同档窗口内不重发——prevTier 变化=换档即新窗口语义照发）。
// lastAlert=nil（从未告警）→ 过冷却闸。data_stale/normal 永不告警。
func shouldMarginAlert(tier, prevTier string, lastAlert *time.Time, urgentStreak int, now time.Time, urgentCD, warnCD time.Duration) bool {
	switch tier {
	case "urgent":
		if urgentStreak < 2 {
			return false // 连续 2 tick 去抖（SSOT §1：防瞬读）
		}
		if lastAlert != nil && prevTier == "urgent" && now.Sub(*lastAlert) < urgentCD {
			return false
		}
		return true
	case "warn":
		if lastAlert != nil && prevTier == "warn" && now.Sub(*lastAlert) < warnCD {
			return false
		}
		return true
	default:
		return false
	}
}
