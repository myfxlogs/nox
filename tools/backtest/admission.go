// admission.go —— 回放窗数据质量准入闸（D-625 P3——P1 覆盖账本消费面）：
// 窗内行数/断档/会话对齐/衍生轴非空率不达 → 「该窗不可判」如实报，禁产假 PnL。
// 阈口径与 P1 覆盖账本同源（饥饿=行数阈、断供=采集周期×3）。
package main

import (
	"fmt"
	"time"
)

// BookRow 回放输入行（fin_xasset_monitor 窗内投影——判定链冻结历史输出）。
type BookRow struct {
	TS               time.Time
	EntrySpreadBps   float64
	ExitSpreadBps    float64
	EntryNetBps      float64
	FundingAnnualPct float64
	NoiseBps         *float64 // NULL=衍生轴缺计（coverage noise_nulls 同源）
}

// WindowStats 窗数据质量画像（报告头覆盖证明字段）。
// GapHit 断档命中点（诊断报文带命中 gap 列表——票面②）。
type GapHit struct {
	At  time.Time // 断档起点（前一行 ts）
	Dur time.Duration
}

type WindowStats struct {
	Rows           int
	FirstTS        time.Time
	LastTS         time.Time
	MaxGap         time.Duration
	GapBreaches    int      // 相邻行距 > cadence×3 次数（断供口径同 P1）
	Breaches       []GapHit // 越 cadence×3 行距命中点（诊断列表用，封顶 20）
	NoiseNulls     int      // noise 缺计行数
	CoverageRate   float64  // 实得行数/开市期望行数（会话对齐率；会话未知=1 免判）
	SessionKnown   bool
	SessionOpenMin int
}

// SessionSource 会话开市分钟读口（marketstate.Table 适配；测试桩替身）。
type SessionSource interface {
	OpenMinutes(venue, symbol string, from, to time.Time) (mins int, known bool)
}

// WindowStatsFrom 行序→窗画像（rows 须按 ts 升序）。
func WindowStatsFrom(rows []BookRow, cadence time.Duration) WindowStats {
	st := WindowStats{Rows: len(rows)}
	if len(rows) == 0 {
		return st
	}
	st.FirstTS, st.LastTS = rows[0].TS, rows[len(rows)-1].TS
	for i := 1; i < len(rows); i++ {
		gap := rows[i].TS.Sub(rows[i-1].TS)
		if gap > st.MaxGap {
			st.MaxGap = gap
		}
		if gap > cadence*3 {
			st.GapBreaches++
			if len(st.Breaches) < 20 {
				st.Breaches = append(st.Breaches, GapHit{At: rows[i-1].TS, Dur: gap})
			}
		}
	}
	for _, r := range rows {
		if r.NoiseBps == nil {
			st.NoiseNulls++
		}
	}
	return st
}

// Admit 准入判定：不可判=verdict false+reasons（如实缺因——禁产假 PnL 的第一闸）。
// 阈口径：minRows=24h 饥饿阈（P1 同源 12/24h 折算窗长）；断档阈=gapMax
// （口径双修②参数化——app_config backtest_gap_min 供给，缺省 15min 兜底；
// 原 cadence×3 对真实窗偏苛实证=单次 25min 断档拒整窗 280 行健康窗）。
func Admit(st WindowStats, sess SessionSource, mtVenue, mtCanon string,
	window time.Duration, cadence time.Duration, gapMax time.Duration) (ok bool, reasons []string) {
	minRows := int(window / (24 * time.Hour) * 12)
	if minRows < 1 {
		minRows = 1
	}
	maxStale := gapMax
	if st.Rows < minRows {
		reasons = append(reasons, fmt.Sprintf("饥饿：窗内 %d 行 <%d（12 行/24h 阈同源折算）", st.Rows, minRows))
	}
	if st.MaxGap > maxStale {
		reasons = append(reasons, fmt.Sprintf("断档：最大行距 %s >%s（backtest_gap_min 阈）", st.MaxGap.Round(time.Minute), maxStale))
	}
	if st.Rows > 0 && st.NoiseNulls*2 > st.Rows {
		reasons = append(reasons, fmt.Sprintf("衍生轴缺计：noise NULL %d/%d 行（>50%%）", st.NoiseNulls, st.Rows))
	}
	// 会话对齐：MT 腿会话已知时，实得行数 vs 开市期望行数（cadence 节拍）。
	if sess != nil {
		if mins, known := sess.OpenMinutes(mtVenue, mtCanon, st.FirstTS, st.LastTS); known {
			st.SessionKnown = true
			st.SessionOpenMin = mins
			expected := mins / (int(cadence) / int(time.Minute))
			if expected < 1 {
				expected = 1
			}
			st.CoverageRate = float64(st.Rows) / float64(expected)
			if st.CoverageRate < 0.5 {
				reasons = append(reasons, fmt.Sprintf("会话对齐缺：开市 %dmin 期望 %d 行实得 %d（对齐率 %.0f%%<50%%）",
					mins, expected, st.Rows, st.CoverageRate*100))
			}
		}
	}
	return len(reasons) == 0, reasons
}
