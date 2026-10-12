// classify.go D-464 S2：四分类 + mispaired 检出（全确定性，零 AI）。
package symdig

import (
	"context"
	"fmt"
	"log"
	"strings"

	"arb/internal/fact"
	"arb/internal/scan"
	"arb/internal/store/pgstore"
)

// 分类标签（findings.klass 值域）。
const (
	KlassPaired          = "paired"
	KlassUnpaired        = "unpaired"
	KlassDisabled        = "disabled"
	KlassUnmappedUnknown = "unmapped_unknown"
	KlassMispaired       = "mispaired"
	// KlassUnitUnknown D-594 S4：单位敏感 canonical（存在非 1 标定行）的未标定行
	// （price_scale/qty_scale NULL）——canonical 槽禁占的亮灯项，宁缺不混。
	KlassUnitUnknown = "unit_unknown"
)

// classify 全量分类：trade_mode facts 逐 venue×symbol 归类 + 映射校验段。
func classify(ctx context.Context, st Store) (*Summary, error) {
	tmFacts, err := st.LatestFactsByKind(ctx, fact.KindTradeMode)
	if err != nil {
		return nil, fmt.Errorf("trade_mode facts: %w", err)
	}
	aliases, err := st.ListSymbolAliases(ctx)
	if err != nil {
		return nil, fmt.Errorf("aliases: %w", err)
	}
	uniPairs, err := st.ListCarryUniversePairs(ctx)
	if err != nil {
		return nil, fmt.Errorf("universe pairs: %w", err)
	}
	cexCanon := map[string]bool{} // CEX funding canonical 集（unpaired 归因用）
	fundFacts, err := st.LatestFactsByKind(ctx, fact.KindFunding)
	if err != nil {
		return nil, fmt.Errorf("funding facts: %w", err)
	}
	for _, f := range fundFacts {
		if c := scan.NormalizeJoinKey(cexLocal(f.Symbol)); c != "" {
			cexCanon[aliasesOr(c, aliases)] = true
		}
	}
	maps, err := st.ListSymbolMappings(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("symbol mappings: %w", err)
	}
	byVenueMap := map[string]map[string]string{} // venue → broker_symbol → canonical
	primaryCount := map[string]int{}             // venue|canonical → is_primary 行数
	sensitive := map[string]bool{}               // canonical → 单位敏感（D-594：任一标定行 scale≠1）
	uncalibrated := map[string]bool{}            // venue|broker_symbol → 敏感 canonical 未标定行
	for _, m := range maps {
		if (m.PriceScale != nil && *m.PriceScale != 1.0) || (m.QtyScale != nil && *m.QtyScale != 1.0) {
			sensitive[m.Canonical] = true
		}
	}
	for _, m := range maps {
		vm := byVenueMap[m.Venue]
		if vm == nil {
			vm = map[string]string{}
			byVenueMap[m.Venue] = vm
		}
		vm[m.BrokerSymbol] = m.Canonical
		if m.IsPrimary && m.Canonical != "" {
			primaryCount[m.Venue+"|"+scan.NormalizeJoinKey(m.Canonical)]++
		}
		if sensitive[m.Canonical] && (m.PriceScale == nil || m.QtyScale == nil) {
			uncalibrated[m.Venue+"|"+m.BrokerSymbol] = true
		}
	}

	sum := &Summary{ByKlass: map[string]int{}}
	add := func(v, sym, klass, reason string, payload map[string]any) {
		sum.Rows = append(sum.Rows, pgstore.SymdigFinding{
			Venue: v, Symbol: sym, Klass: klass, Reason: reason, Payload: payload})
		sum.ByKlass[klass]++
	}
	for _, f := range tmFacts {
		venue, sym, mode := f.Venue, f.Symbol, int64(f.Value)
		payload := map[string]any{"trade_mode": mode}
		if mode == fact.TradeModeDisabled {
			add(venue, sym, KlassDisabled, "trade_mode=0", payload)
			continue
		}
		if !tradableMode(mode) { // CloseOnly 等非新开仓态
			add(venue, sym, KlassDisabled, "trade_mode="+fmt.Sprint(mode), payload)
			continue
		}
		// canonical 须有据（map 命中或别名命中）；裸归一 raw symbol 不算映射线索。
		canonical := aliasesOr(scan.NormalizeJoinKey(byVenueMap[venue][sym]), aliases)
		if canonical == "" {
			if v, ok := aliases[scan.NormalizeJoinKey(sym)]; ok {
				canonical = v
			}
		}
		if canonical == "" {
			add(venue, sym, KlassUnmappedUnknown, "unmapped", payload)
			continue
		}
		payload["canonical"] = canonical
		// D-594 S4：映射行未标定且 canonical 单位敏感 → unit_unknown（配对池判定前拦截，
		// 报价发射侧 emitPlan 已同步抑制其 canonical 槽）。
		if uncalibrated[venue+"|"+sym] {
			add(venue, sym, KlassUnitUnknown, "unit uncalibrated (price_scale/qty_scale NULL)", payload)
			continue
		}
		if uniPairs[venue+"|"+canonical] {
			add(venue, sym, KlassPaired, "", payload)
			continue
		}
		reason := "filtered"
		if !cexCanon[canonical] {
			reason = "no_cex_counterpart"
		}
		add(venue, sym, KlassUnpaired, reason, payload)
	}
	// 已配对校验段：同 venue 同 canonical 多 is_primary → mispaired。
	for k, n := range primaryCount {
		if n > 1 {
			parts := strings.SplitN(k, "|", 2)
			add(parts[0], parts[1], KlassMispaired, "multi_primary", map[string]any{"canonical": parts[1], "primary_rows": n})
		}
	}
	// diff：较上一轮新增的非 paired/disabled 行=告警对象。
	prev, err := st.LatestSymdigKeys(ctx)
	if err != nil {
		log.Printf("[symdig] prev keys: %v（视同空集全量告警）", err)
	}
	for _, r := range sum.Rows {
		if r.Klass == KlassPaired || r.Klass == KlassDisabled {
			continue
		}
		key := r.Venue + "|" + r.Symbol + "|" + r.Klass
		if !prev[key] {
			sum.NewUnpaired = append(sum.NewUnpaired, key)
		}
	}
	return sum, nil
}

// cexLocal CEX symbol→canonical 粗归一（与 scan.cexCanonical 同规则，独立实现防包耦合）。
func cexLocal(symbol string) string {
	if strings.HasSuffix(symbol, "USDT") && len(symbol) > 4 {
		return strings.TrimSuffix(symbol, "USDT")
	}
	return ""
}

// aliasesOr alias 命中→canonical；空输入保持空。
func aliasesOr(c string, aliases map[string]string) string {
	if c == "" {
		return ""
	}
	if v, ok := aliases[c]; ok && v != "" {
		return v
	}
	return c
}

// joinLimit 告警消息截断。
func joinLimit(ss []string, n int) string {
	if len(ss) > n {
		return strings.Join(ss[:n], ",") + fmt.Sprintf(",...(+%d)", len(ss)-n)
	}
	return strings.Join(ss, ",")
}
