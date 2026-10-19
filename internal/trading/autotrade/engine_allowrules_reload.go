// engine_allowrules_reload.go D-443 S2：venue/canonical 执行白名单热读。
// 读点 allowVenues/allowCanonicals（atomic Load）vs 写点 ReloadAllowRules（atomic Store）
// 竞态安全，同 engine_xasset_reload.go 模式。nil 指针（零值 Engine 直构测试路径）回退 cfg。
package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"

	"nox/internal/store/pgstore"
)

// allowRules venue/canonical 白名单快照（指针整体替换）。
type allowRules struct {
	venues     map[string]bool
	canonicals map[string]bool
}

// allowVenues 当前 venue 白名单（热读优先；nil 回退 cfg 启动值）。
func (e *Engine) allowVenues() map[string]bool {
	if p := e.allowRules.Load(); p != nil {
		return p.venues
	}
	return e.cfg.AllowVenues
}

// allowCanonicals 当前 canonical 白名单（空集=全放行 D-122 语义不动）。
func (e *Engine) allowCanonicals() map[string]bool {
	if p := e.allowRules.Load(); p != nil {
		return p.canonicals
	}
	return e.cfg.AllowCanonicals
}

// allowRuleReject venue/canonical 双腿闸（engine_opp.handleOpportunity 调用点；
// 返回值非空=拒因文案）。空 venue 集=全拒（fail-closed 最保守）。
func (e *Engine) allowRuleReject(opp pgstore.Opportunity) string {
	v := e.allowVenues()
	if !v[opp.VenueBuy] || !v[opp.VenueSell] {
		return fmt.Sprintf("venue not in allowlist (%s/%s)", opp.VenueBuy, opp.VenueSell)
	}
	if c := e.allowCanonicals(); len(c) > 0 && !c[opp.Canonical] {
		return fmt.Sprintf("canonical not in allowlist (%s)", opp.Canonical)
	}
	return ""
}

// ReloadAllowRules D-443 S2：重跑 LoadAutotradeAllowRules + 指针交换（wire 60s ticker 调用，
// 业主 SQL 改行 → ≤60s 生效免重启）。simulated 闸 = Environment=='demo'。
func (e *Engine) ReloadAllowRules(ctx context.Context) error {
	venues, canonicals, err := e.store.LoadAutotradeAllowRules(ctx, e.cfg.Environment == "demo")
	if err != nil {
		return fmt.Errorf("reload autotrade allow rules: %w", err)
	}
	e.allowRules.Store(&allowRules{venues: venues, canonicals: canonicals})
	log.Printf("[whitelist] autotrade rules reload: venues=%d canonicals=%d (D-443 热重载免重启)", len(venues), len(canonicals))
	e.venueGapSweep(ctx, venues)
	return nil
}

// venueGapSweep 新户入册放行/闸行缺口扫（reload 60s 轴搭车）：在册
// venue_key（exchange_configs+broker_accounts 两源）缺放行单 kind='venue'
// 行或缺 fin_venue_exposure_caps 闸行→constraint 型 condition_gap（幂等
// UNIQUE(template_id,kind,subject)——新户落账即自动出箱非人工作业）。
// 禁线：扫行不建闸/放行行本体（授权面=决策席裁定，本扫只出缺口箱）。
func (e *Engine) venueGapSweep(ctx context.Context, allow map[string]bool) {
	if e.store == nil {
		return
	}
	keys, err := e.store.ListOnboardVenueKeys(ctx)
	if err != nil {
		log.Printf("[venue-gap] onboard keys read: %v", err)
		return
	}
	caps, err := e.store.ListVenueExposureCaps(ctx)
	if err != nil {
		log.Printf("[venue-gap] exposure caps read: %v", err)
		return
	}
	for _, k := range keys {
		var miss []string
		if !allow[k] {
			miss = append(miss, "autotrade_allow_rules 放行行")
		}
		if _, ok := caps[k]; !ok {
			miss = append(miss, "fin_venue_exposure_caps 闸行")
		}
		if len(miss) == 0 {
			continue
		}
		id, err := e.store.OpenConditionGap(ctx, &pgstore.ConditionGap{
			TemplateID: "venue_onboard", Kind: "constraint", Subject: k,
			RequestText: fmt.Sprintf("新户 venue_key=%s 在册但缺 %s——放行/闸行缺行机制化落箱（决策席裁定后 SQL 补行或 waive）", k, strings.Join(miss, "+"))})
		if err != nil {
			log.Printf("[venue-gap] open %s: %v", k, err)
			continue
		}
		if err := e.store.RequestConditionGap(ctx, id); err != nil {
			log.Printf("[venue-gap] request %s#%d: %v", k, id, err)
		}
	}
}
