// depth_section.go —— D-578 S2：三所 spot 深度采样章节（facts src=cex_depth_sampler 聚合，
// Go 确定性算好后喂周报/日报 fenced 块；AI 只叙事照表说话）。
package sysretro

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

const (
	depthSpreadKind = "spot_depth_spread_bps"
	depthBidKind    = "spot_depth_bid_usd"
	depthAskKind    = "spot_depth_ask_usd"
)

// depthCell 单 (venue,symbol) 三指标样本集。
type depthCell struct {
	spread, bid, ask []float64
	worstSpread      float64 // spread 最差时点值
	worstTs          time.Time
	bidWorst         float64 // bid 深度最差时点值
	bidWorstTs       time.Time
	askWorst         float64
	askWorstTs       time.Time
	ticks            map[int64]bool
}

// groupDepth 按 venue×symbol 归集采样行（venue_key 去 :spot 后缀为展示名）。
func groupDepth(rows []pgstore.DepthFact) map[string]*depthCell {
	out := map[string]*depthCell{}
	for _, r := range rows {
		venue := strings.TrimSuffix(r.Venue, ":spot")
		key := venue + " " + r.Symbol
		c := out[key]
		if c == nil {
			c = &depthCell{ticks: map[int64]bool{}}
			out[key] = c
		}
		c.ticks[r.Ts.Unix()] = true
		switch r.Kind {
		case depthSpreadKind:
			c.spread = append(c.spread, r.Value)
			if len(c.spread) == 1 || r.Value > c.worstSpread {
				c.worstSpread, c.worstTs = r.Value, r.Ts
			}
		case depthBidKind:
			c.bid = append(c.bid, r.Value)
			if len(c.bid) == 1 || r.Value < c.bidWorst {
				c.bidWorst, c.bidWorstTs = r.Value, r.Ts
			}
		case depthAskKind:
			c.ask = append(c.ask, r.Value)
			if len(c.ask) == 1 || r.Value < c.askWorst {
				c.askWorst, c.askWorstTs = r.Value, r.Ts
			}
		}
	}
	return out
}

func medianOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func pctile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(p * float64(len(s)-1))
	return s[i]
}

func meanOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// depthWeeklyTable 周报「三所深度（近 7 日）」表：每 venue×symbol 均值/中位/p10/p90/最差时点；
// 零行对标无此对；拍数<4 注采样中（不猜分布）。
func depthWeeklyTable(rows []pgstore.DepthFact) string {
	cells := groupDepth(rows)
	var b strings.Builder
	keys := make([]string, 0, len(cells))
	for k := range cells {
		keys = append(keys, k)
	}
	// 未出现的 venue×canonical 组合也要标（XAUT/PAXG 起步集三所全枚举）。
	present := map[string]bool{}
	for k := range cells {
		present[k] = true
	}
	for _, sym := range []string{"XAUT", "PAXG"} {
		for _, v := range []string{"binance", "gate", "okx"} {
			k := v + " " + sym
			if !present[k] {
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		c := cells[k]
		if c == nil || len(c.ticks) == 0 {
			fmt.Fprintf(&b, "%s: 无此对或采样行=0\n", k)
			continue
		}
		note := ""
		if len(c.ticks) < 4 {
			note = fmt.Sprintf("（采样中 %d 拍）", len(c.ticks))
		}
		fmt.Fprintf(&b, "%s n=%d%s | spread_bps avg=%.2f med=%.2f p10=%.2f p90=%.2f worst=%.2f@%s | bid_usd avg=%.0f med=%.0f p10=%.0f p90=%.0f worst=%.0f@%s | ask_usd avg=%.0f med=%.0f p10=%.0f p90=%.0f worst=%.0f@%s\n",
			k, len(c.ticks), note,
			meanOf(c.spread), medianOf(c.spread), pctile(c.spread, 0.1), pctile(c.spread, 0.9), c.worstSpread, c.worstTs.Format("01-02 15:04"),
			meanOf(c.bid), medianOf(c.bid), pctile(c.bid, 0.1), pctile(c.bid, 0.9), c.bidWorst, c.bidWorstTs.Format("01-02 15:04"),
			meanOf(c.ask), medianOf(c.ask), pctile(c.ask, 0.1), pctile(c.ask, 0.9), c.askWorst, c.askWorstTs.Format("01-02 15:04"))
	}
	return b.String()
}

// depthDailyLine 日报一行快照：当窗各 venue×symbol 中位深度/点差；零行=无此对；全空=采样中。
func depthDailyLine(rows []pgstore.DepthFact) string {
	cells := groupDepth(rows)
	if len(cells) == 0 {
		return "三所深度当日: 采样中（0 拍）"
	}
	var parts []string
	for _, sym := range []string{"XAUT", "PAXG"} {
		for _, v := range []string{"binance", "gate", "okx"} {
			c := cells[v+" "+sym]
			if c == nil || len(c.ticks) == 0 {
				parts = append(parts, fmt.Sprintf("%s %s=无此对", v, sym))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s %s 买深$%.0f/卖深$%.0f 点差%.2fbps",
				v, sym, medianOf(c.bid), medianOf(c.ask), medianOf(c.spread)))
		}
	}
	return "三所深度当日: " + strings.Join(parts, " | ")
}
