// digest.go —— D-366 S1（R3）：radar D-369 块自 main.go 整组移出（同包纯搬移零行为
// 变更；块体与 efc80ab7 逐字节一致，仅包一层函数签名）。
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// d369RouteFundingEvents D-369 S1/S2：funding band 事件路由（leg=无 → digest 累加，
// 有腿 → 实时推送）+ radar_watchlist 写侧（divergence 双腿/有腿 band 单腿）。
// D-389 S3：冷却戳不再即时落库——收集进 touches 由 pushAndBookkeep 在发送成功后落
// （R0-A2：发送失败=信号已冷却却没推出=静默丢 24h 前置缺陷收编）；legless digest
// 行走 pushDigestIfChanged 边沿触发（集合不变不推）。
func d369RouteFundingEvents(ctx context.Context, st radarStore, rows []normRow,
	old map[string]pgstore.CexUniverseRow, events []pgstore.CexEvent, now time.Time,
	fundingMinPct, volMinUSD float64, lines *[]string,
	stored map[string]string, pending map[string]string, touches *[]alertTouch) {

	// D-369 S1（D1 裁定）：leg=无 的 funding band 事件不进实时推送（单腿信号噪声；
	// 可行动信号由 FUNDING_DIVERGENCE 单独报），改累计进 digest 汇总行（每轮至多一条）。
	legless := []fundingEvent{}
	for _, ev := range fundingTransitions(rows, old, fundingMinPct, volMinUSD) {
		if ev.row.HedgeCanonical == "" {
			legless = append(legless, ev)
			continue
		}
		if o, ok := old[ev.row.Venue+"|"+ev.row.Symbol]; ok && !cooldownOK(o.LastAlertTS, now) {
			continue
		}
		*lines = append(*lines, fundingEventLine(ev))
		*touches = append(*touches, alertTouch{ev.row.Venue, ev.row.Symbol})
	}
	if len(legless) > 0 {
		syms := make([]string, 0, len(legless))
		for _, ev := range legless {
			syms = append(syms, ev.row.Symbol)
		}
		pushDigestIfChanged(stored, pending, lines, "legless_band", syms,
			fmt.Sprintf("[universe-radar] %d 条 leg=无 funding band 事件已压 digest（%s）",
				len(legless), strings.Join(syms, ",")))
		// 集合判变且行已入队才盖冷却戳（行未推时不应抑制后续真边沿）。
		if _, queued := pending["legless_band"]; queued {
			for _, ev := range legless {
				*touches = append(*touches, alertTouch{ev.row.Venue, ev.row.Symbol})
			}
		}
	}

	// D-369 S2 radar 写侧：divergence 事件与有腿 band 事件 → radar_watchlist upsert
	// （确认信号单一源：radar 写证据、core 读 feed+combo seed）。
	for _, e := range events {
		if e.EventType == "FUNDING_DIVERGENCE" {
			// Detail 首段=“venueA:venueB”（diff.go divergences() 构造序）
			pair := strings.SplitN(e.Detail, " ", 2)[0]
			parts := strings.Split(pair, ":")
			if len(parts) == 2 {
				if err := st.UpsertRadarWatchlist(ctx, e.Symbol, []string{parts[0], parts[1]}, "divergence"); err != nil {
					log.Printf("universe-radar: watchlist upsert %s: %v", e.Symbol, err)
				}
			}
		}
	}
	for _, ev := range fundingTransitions(rows, old, fundingMinPct, volMinUSD) {
		if ev.row.HedgeCanonical == "" {
			continue // leg=无：结构性不可执行，不入 watchlist（D-369 D1）
		}
		if err := st.UpsertRadarWatchlist(ctx, ev.row.Symbol, []string{ev.row.Venue}, "band_with_leg"); err != nil {
			log.Printf("universe-radar: watchlist upsert %s: %v", ev.row.Symbol, err)
		}
	}
}
