// engine_flags_reload.go D-475a S2：fin_venue_symbol_flags 执行层投影——
// 读点 flagRuleReject（handleOpportunity）vs 写点 ReloadFlags（atomic Store）
// 竞态安全，逐行仿 engine_allowrules_reload.go。
package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"

	"arb/internal/store/pgstore"
)

// flagSnapshot 旗标按 level 分桶快照（key=pgstore.FlagKey(venue,symbol)）。
type flagSnapshot struct {
	blocked map[string]pgstore.VenueSymbolFlag
	warned  map[string]pgstore.VenueSymbolFlag
}

// bareVenue venue_key→裸名归一（'3001157:mt5'→'3001157'、'okx:perp'→'okx'，
// 与旗标 venue 口径对齐——0138 迁移注释钉裸名）。
func bareVenue(venueKey string) string {
	if i := strings.Index(venueKey, ":"); i >= 0 {
		return venueKey[:i]
	}
	return venueKey
}

// flagRuleReject 旗标闸（allowRuleReject 后调用；返回非空=拒因）。
// 候选 venue 集={bareBuy, bareSell, bareBuy×bareSell, bareSell×bareBuy}——
// 与 scan.flagVenueSet 展开口径对齐：reject 写口落旗 venue=纸面组合串
// （'3001157×okx' 生产实证），漏组合串级匹配=对唯一写源哑火；双向序盖
// perp_perp 无序对。symbol 维=腿 canonical（买腿 Canonical，卖腿
// CanonicalSell 非空取之否则 Canonical）+venue 级 '*' 通配。
// nil 快照=空集放行（boot 装配先载+fatal 兜底；旗标缺席=无风险信号）。
func (e *Engine) flagRuleReject(opp pgstore.Opportunity) string {
	snap := e.flags.Load()
	if snap == nil {
		return ""
	}
	buy, sell := bareVenue(opp.VenueBuy), bareVenue(opp.VenueSell)
	venues := []string{buy, sell, buy + "×" + sell, sell + "×" + buy}
	sellCanon := opp.Canonical
	if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
		sellCanon = *opp.CanonicalSell
	}
	for _, v := range venues {
		for _, sym := range []string{opp.Canonical, sellCanon, "*"} {
			if f, ok := snap.blocked[pgstore.FlagKey(v, sym)]; ok {
				return fmt.Sprintf("blocked flag %s:%s %s", v, sym, f.Reason)
			}
		}
	}
	for _, v := range venues {
		for _, sym := range []string{opp.Canonical, sellCanon, "*"} {
			if f, ok := snap.warned[pgstore.FlagKey(v, sym)]; ok {
				log.Printf("[autotrade] flag warn %s:%s %s（放行不拦）", v, sym, f.Reason)
			}
		}
	}
	return ""
}

// ReloadFlags 重载旗标快照（boot 首载+configKick/5min ticker/写旗即踢同口）。
func (e *Engine) ReloadFlags(ctx context.Context) error {
	all, err := e.store.ListActiveFlags(ctx)
	if err != nil {
		return fmt.Errorf("reload venue symbol flags: %w", err)
	}
	snap := &flagSnapshot{
		blocked: map[string]pgstore.VenueSymbolFlag{},
		warned:  map[string]pgstore.VenueSymbolFlag{},
	}
	for k, f := range all {
		switch f.Level {
		case "blocked":
			snap.blocked[k] = f
		case "warn":
			snap.warned[k] = f
		}
	}
	e.flags.Store(snap)
	log.Printf("[flags] reload: blocked=%d warn=%d", len(snap.blocked), len(snap.warned))
	return nil
}
