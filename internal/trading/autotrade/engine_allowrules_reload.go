// engine_allowrules_reload.go D-443 S2：venue/canonical 执行白名单热读。
// 读点 allowVenues/allowCanonicals（atomic Load）vs 写点 ReloadAllowRules（atomic Store）
// 竞态安全，同 engine_xasset_reload.go 模式。nil 指针（零值 Engine 直构测试路径）回退 cfg。
package autotrade

import (
	"context"
	"fmt"
	"log"

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
	return nil
}
