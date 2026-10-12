// digest.go —— D-366 S1（R3）：radar D-369 块自 main.go 整组移出（同包纯搬移零行为
// 变更；块体与 efc80ab7 逐字节一致，仅包一层函数签名）。
package main

import (
	"context"
	"log"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// d369RouteFundingEvents D-369 S1/S2：funding band 事件路由（leg=无 → digest 累加，
// 有腿 → 实时推送）+ radar_watchlist 写侧（divergence 双腿/有腿 band 单腿）。
// 冷却戳不再即时落库——收集进 touches 由 pushAndBookkeep 在发送成功后落
// （R0-A2：发送失败=信号已冷却却没推出=静默丢 24h 前置缺陷收编）；legless digest
// 行走 pushDigestNewMembers 成员级边沿触发（D-392：新入才推）。
func d369RouteFundingEvents(ctx context.Context, st radarStore, rows []normRow,
	old map[string]pgstore.CexUniverseRow, events []pgstore.CexEvent, now time.Time,
	fundingMinPct, volMinUSD float64, lines *[]string,
	stored map[string]map[string]pgstore.RadarDigestMember, cur map[string][]string,
	pending map[string][]string, touches *[]alertTouch) {

	// S1（D1 裁定）：leg=无 的 funding band 事件不进实时推送（单腿信号噪声；
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
	syms := make([]string, 0, len(legless))
	for _, ev := range legless {
		syms = append(syms, ev.row.Symbol)
	}
	cur["legless_band"] = syms
	pushDigestNewMembers(stored["legless_band"], pending, lines, "legless_band", syms,
		func(m string) string { return m }, now)
	// 冷却戳成员粒度化——只给本轮被推行的新入成员盖戳（被压成员的冷却
	// 不消耗：未推不抑制其后续真边沿；拿到腿后进场仍可实时报）。
	if pushed := pending["legless_band"]; len(pushed) > 0 {
		pushedSet := map[string]bool{}
		for _, m := range pushed {
			pushedSet[m] = true
		}
		for _, ev := range legless {
			if pushedSet[ev.row.Symbol] {
				*touches = append(*touches, alertTouch{ev.row.Venue, ev.row.Symbol})
			}
		}
	}

	// radar 写侧：divergence 事件与有腿 band 事件 → radar_watchlist upsert
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
		// 写 canonical 基名（ev.row.Canonical=canonicalOf(normBase)，与 divergence
		// 写侧同方言；HedgeCanonical=对冲腿 canonical 不可作本行 symbol）。原 ev.row.Symbol
		// 为 venue 原生形态（-USDT-SWAP/_USDT/USDT 尾缀混写已致方言分裂）。
		if err := st.UpsertRadarWatchlist(ctx, ev.row.Canonical, []string{ev.row.Venue}, "band_with_leg"); err != nil {
			log.Printf("universe-radar: watchlist upsert %s: %v", ev.row.Canonical, err)
		}
	}
}
