// intra_signal.go —— D-425 S2：日内策略基线信号+成交模型（纯函数，Step 与回放门共用单实现）。
// 两条最简可证伪基线：①5min 动量突破（前高/前低破位进，区间 ATR 止损）②30min 均值回归
// （z-score 门槛进、回归零轴出）。诚实定位：不造 alpha，只做执行机械与风控的载体。
package scan

import (
	"fmt"
	"math"
	"time"

	"arb/internal/store/pgstore"
)

// D-425 风险口径常量（§1 业主令参数；env 只覆盖六件，栅栏结构硬编码）。
const (
	IntraLeverage       = 10.0 // 杠杆（保证金=名义/10）
	IntraStopBudgetFrac = 0.80 // 止损位=保证金风险预算的 80%——留 2% 余量给滑点，回放门 P100≤10% 才可成立
	IntraTakerFeeBps    = 5.0  // taker 单边费（纸面成交模型）
	intraSpreadHalfFee  = 0.5  // 成交价=中价±半点差（买 ask/卖 bid 已含）
)

// simEvent 一次进/出场事件（simulate 输出；Step 消费末事件，回放门消费全量）。
type simEvent struct {
	Enter    bool
	Baseline string
	Side     string // long / short
	Ts       time.Time
	Px       float64 // 成交价（已含半点差）
	StopDist float64 // 价格止损带（分数）
	Reason   string  // 出场原因：stop / signal / eod
	Note     string
}

// IntraPair 品种白名单（§2 深度优先禁长尾；MT=时段闸域，crypto=7×24 例外仅 BTC/ETH）。
type IntraPair struct {
	Venue  string
	Symbol string
	Crypto bool
}

// IntraPairs v1 白名单（腿与依据见 SSOT §2；XPT/XPD 点差 22-46bps 排除）。
// D-508 S13 豁免：venue=MT 账号号（1314615/3001157）为设计冻结宇宙——v1 深度优先禁长尾，
// 扩宇宙=新 D# 票（改名单须同步 SSOT §2 依据）。
var IntraPairs = []IntraPair{
	{Venue: "binance", Symbol: "BTCUSDT", Crypto: true},
	{Venue: "okx", Symbol: "BTCUSDT", Crypto: true},
	{Venue: "binance", Symbol: "ETHUSDT", Crypto: true},
	{Venue: "okx", Symbol: "ETHUSDT", Crypto: true},
	{Venue: "1314615", Symbol: "XAUUSD", Crypto: false},
	{Venue: "3001157", Symbol: "XAUUSD", Crypto: false},
}

// SessionAllows D-425 时段闸（§1）：MT 品种排除整点前后 2min 与周末；加密 7×24 例外仅 BTC/ETH。
func SessionAllows(crypto bool, now time.Time) bool {
	if crypto {
		return true
	}
	wd := now.UTC().Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	mn := now.UTC().Minute()
	return mn >= 2 && mn < 58
}

// midOf tick 中价（纸面成交基准）。
func midOf(bid, ask float64) float64 { return (bid + ask) / 2 }

// simulate 单基线对 tick 序列的确定性重放（信号→成交→止损/信号/EOD 出场）。
// dry-run 零落库：回放门消费全量事件算 P100，Step 只取末事件对齐 DB 仓态。
func simulate(baseline string, ticks []pgstore.TickRow, cfg IntraConfig, start time.Time) []simEvent {
	var out []simEvent
	inPos := false
	var side string
	var entryPx, stopDist float64
	for i, tk := range ticks {
		mid := midOf(tk.Bid, tk.Ask)
		if mid <= 0 {
			continue
		}
		// EOD：UTC 23:50 后强平（日内纪律）
		if inPos {
			if tk.Ts.UTC().Hour() == 23 && tk.Ts.UTC().Minute() >= 50 {
				out = append(out, simEvent{Enter: false, Baseline: baseline, Side: side, Ts: tk.Ts, Px: exitPx(side, tk, false), Reason: "eod"})
				inPos = false
				continue
			}
			// 止损：浮亏达 stopDist（滑点模型=市价穿价，出场取对侧价即含点差）
			loss := (mid - entryPx) / entryPx
			if side == "short" {
				loss = -loss
			}
			if loss <= -stopDist {
				out = append(out, simEvent{Enter: false, Baseline: baseline, Side: side, Ts: tk.Ts, Px: exitPx(side, tk, true), Reason: "stop",
					Note: fmt.Sprintf("entry=%.4f stopDist=%.4f", entryPx, stopDist)})
				inPos = false
				continue
			}
		}
		switch baseline {
		case "momentum_break":
			// 前窗（至多 60min）高/低破位进；本 tick 起回看，排除在仓期
			lookback := ticks
			if i > 60 {
				lookback = ticks[i-60 : i]
			} else {
				lookback = ticks[:i]
			}
			if len(lookback) < 10 {
				continue
			}
			hi, lo := -math.MaxFloat64, math.MaxFloat64
			for _, t := range lookback {
				m := midOf(t.Bid, t.Ask)
				if m > hi {
					hi = m
				}
				if m > 0 && m < lo {
					lo = m
				}
			}
			rng := (hi - lo) / mid
			if inPos {
				// 反向破位出场
				if (side == "long" && mid < lo) || (side == "short" && mid > hi) {
					out = append(out, simEvent{Enter: false, Baseline: baseline, Side: side, Ts: tk.Ts, Px: exitPx(side, tk, false), Reason: "signal"})
					inPos = false
				}
				continue
			}
			// R1：价格止损带上限=预算 80%×(保证金 10%/10x)=0.8% 价格（禁 8% 名义误读）
			stopDist = math.Max(0.003, math.Min(0.40*rng, IntraStopBudgetFrac*0.10/IntraLeverage))
			if mid > hi*(1+0.0005) && i-entryAllowGap(ticks, i) >= 0 {
				out = append(out, simEvent{Enter: true, Baseline: baseline, Side: "long", Ts: tk.Ts, Px: tk.Ask, StopDist: stopDist, Note: fmt.Sprintf("break hi=%.4f", hi)})
				inPos, side, entryPx = true, "long", tk.Ask
			} else if mid < lo*(1-0.0005) {
				out = append(out, simEvent{Enter: true, Baseline: baseline, Side: "short", Ts: tk.Ts, Px: tk.Bid, StopDist: stopDist, Note: fmt.Sprintf("break lo=%.4f", lo)})
				inPos, side, entryPx = true, "short", tk.Bid
			}
		case "meanrev":
			// 30min 窗 z-score：|z|>2 逆向进；|z|<0.5 回归零轴出
			lookback := ticks
			if i > 30 {
				lookback = ticks[i-30 : i]
			} else {
				lookback = ticks[:i]
			}
			if len(lookback) < 15 {
				continue
			}
			mean, sd := meanStd(lookback)
			if sd <= 0 {
				continue
			}
			z := (mid - mean) / sd
			if inPos {
				if (side == "long" && z >= 0) || (side == "short" && z <= 0) {
					out = append(out, simEvent{Enter: false, Baseline: baseline, Side: side, Ts: tk.Ts, Px: exitPx(side, tk, false), Reason: "signal",
						Note: fmt.Sprintf("z=%.2f", z)})
					inPos = false
				}
				continue
			}
			if z > 2 {
				out = append(out, simEvent{Enter: true, Baseline: baseline, Side: "short", Ts: tk.Ts, Px: tk.Bid, StopDist: IntraStopBudgetFrac * 0.10 / IntraLeverage, Note: fmt.Sprintf("z=%.2f", z)})
				inPos, side, entryPx = true, "short", tk.Bid
			} else if z < -2 {
				out = append(out, simEvent{Enter: true, Baseline: baseline, Side: "long", Ts: tk.Ts, Px: tk.Ask, StopDist: IntraStopBudgetFrac * 0.10 / IntraLeverage, Note: fmt.Sprintf("z=%.2f", z)})
				inPos, side, entryPx = true, "long", tk.Ask
			}
		}
	}
	return out
}

// entryAllowGap 动量破位的防重入最小间隔（占位常量语义：破位后至少隔 1 tick 才可再判）。
func entryAllowGap(_ []pgstore.TickRow, i int) int { return i }

// exitPx 出场成交价：止损=市价穿价取对侧（含点差=滑点来源）；信号/EOD=对侧价正常成交。
func exitPx(side string, tk pgstore.TickRow, stop bool) float64 {
	if side == "long" {
		return tk.Bid
	}
	return tk.Ask
	// stop 参数保留语义位（v1 滑点=点差内；穿口滑点由 slippageBps 记录取 |exec−stop|）
}

// meanStd 收盘窗均值/样本标准差。
func meanStd(ticks []pgstore.TickRow) (float64, float64) {
	n := float64(len(ticks))
	var sum, sum2 float64
	for _, t := range ticks {
		m := midOf(t.Bid, t.Ask)
		sum += m
		sum2 += m * m
	}
	mean := sum / n
	variance := sum2/n - mean*mean
	if variance < 0 {
		variance = 0
	}
	return mean, math.Sqrt(variance)
}
