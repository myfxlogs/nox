package main

// diff_funding.go —— funding 极端带转移事件+冷却纯函数件（原 diff.go 拆分，零行为变）。

import (
	"math"
	"time"

	"nox/internal/store/pgstore"
)

// fundingEventKind D-353 S1：funding 告警事件类型（事件语义——只在状态转移时发声，
// 稳态零报；24h last_alert_ts 冷却保留作 flap 抑制）。
type fundingEventKind string

const (
	fundingNewEntry fundingEventKind = "new_entry" // 本轮新进极端带
	fundingSignFlip fundingEventKind = "sign_flip" // 符号翻转
	fundingExitBand fundingEventKind = "exit_band" // 跌出极端带（行仍在）
)

// fundingEvent funding 转移事件：kind 标签 + 行 + 旧值（文案含旧→新两侧数值）。
type fundingEvent struct {
	row    normRow
	kind   fundingEventKind
	oldAnn *float64 // 转移前 ann（old 缺行/nil → nil）
}

// fundingQualify 极端带判定：|ann|≥pct ∧ vol≥volMin（ann 非 nil）。
func fundingQualify(n normRow, fundingMinPct, volMinUSD float64) bool {
	return n.FundingAnn != nil && math.Abs(*n.FundingAnn) >= fundingMinPct && n.Vol24h >= volMinUSD
}

// fundingTransitions D-353 S1：funding 告警事件化——只在状态转移时发声，稳态零报。
//   - new_entry：本轮 qualify ∧（old 无行 ∨ old 不 qualify）∧ 非翻转；
//   - sign_flip：本轮 qualify ∧ old qualify ∧ 符号相反；
//   - exit_band：old qualify ∧ 本轮 qualify 失效（ann 归常态/归 nil/vol 跌破）——
//     行整体消失不在此列（循环只遍历本轮行；DELISTED 转移事件已单报，防双报）。
//
// 冷却与发送仍在 main.go（last_alert_ts 24h=flap 抑制，进出带抖动至多一报/日）。
func fundingTransitions(rows []normRow, old map[string]pgstore.CexUniverseRow, fundingMinPct, volMinUSD float64) []fundingEvent {
	var out []fundingEvent
	for _, n := range rows {
		key := n.Venue + "|" + n.Symbol
		var oldAnn *float64
		oldQualify := false
		if o, had := old[key]; had {
			oldAnn = o.FundingAnn
			oldQualify = o.FundingAnn != nil && o.Vol24h != nil &&
				math.Abs(*o.FundingAnn) >= fundingMinPct && *o.Vol24h >= volMinUSD
		}
		qual := fundingQualify(n, fundingMinPct, volMinUSD)
		switch {
		case qual && oldQualify:
			if (*oldAnn >= 0) != (*n.FundingAnn >= 0) {
				out = append(out, fundingEvent{row: n, kind: fundingSignFlip, oldAnn: oldAnn})
			}
			// 同号稳态：零事件（每日重报根因即此处退化）
		case qual:
			out = append(out, fundingEvent{row: n, kind: fundingNewEntry, oldAnn: oldAnn})
		case oldQualify:
			out = append(out, fundingEvent{row: n, kind: fundingExitBand, oldAnn: oldAnn})
		}
	}
	return out
}

// cooldownOK last_alert_ts 冷却判定：nil/超过 24h → 可告警。
func cooldownOK(last *time.Time, now time.Time) bool {
	return last == nil || now.Sub(*last) >= 24*time.Hour
}
