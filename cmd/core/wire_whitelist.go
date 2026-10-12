// D-338 S3：XASSET 白名单表加载装配（自 setup.go 拆出保行预算）。
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
	"nox/internal/trading/autotrade"
	"nox/internal/trading/exec"
)

// xassetAllowFatal startup fatal seam（测试 stub 断言错误路径；生产=log.Fatalf fail-closed）。
var xassetAllowFatal = log.Fatalf

// allowRulesFatal autotrade 白名单装配 fatal seam（同 xassetAllowFatal）。
var allowRulesFatal = log.Fatalf

// allowRulesFromTable D-443 S3：autotrade venue/canonical 白名单表加载。
// 加载失败 → fatal（fail-closed）；live 空 venue 集 → fatal（配置错当场炸不静默，
// 仿 setup.go:99 combos 先例）；demo 空集 → warn（合法态=autotrade 开仓全拒）。
// canonical 空集=全放行（D-122 语义不动，不计 fatal）。
func allowRulesFromTable(ctx context.Context, st *pgstore.Store, environment string) (venues, canonicals map[string]bool) {
	venues, canonicals, err := st.LoadAutotradeAllowRules(ctx, environment == "demo")
	if err != nil {
		allowRulesFatal("[whitelist] autotrade rules load: %v (startup fail-closed)", err)
	}
	if len(venues) == 0 {
		if environment == "live" {
			allowRulesFatal("[whitelist] autotrade venues empty env=live (fail-closed：表空=开仓全拒，配置错当场炸)")
		}
		log.Printf("[whitelist] autotrade venues empty env=%s (demo warn：autotrade 开仓全拒)", environment)
	}
	log.Printf("[whitelist] autotrade rules env=%s venues=%d canonicals=%d", environment, len(venues), len(canonicals))
	return venues, canonicals
}

// startAllowRulesReloader D-443 S3+D-469b S5+D-475a S4/S6：configKick 事件主路
// （mutation RPC 即发即载）+5min ticker 兜底。kick/ticker 两触发点同载三件：
// allowRules + flags（D-475a S2 执行投影）+ xasset_combos（S4 带外改行兜住——
// 手工 UPDATE xasset_allow_combos 引擎不刷新的现存洞与吊销漏网同治）。
func startAllowRulesReloader(ctx context.Context, e *autotrade.Engine, kick <-chan struct{}, pushSwapArb func([]exec.ComboAllowSpec)) {
	safego.GoLoop("main:allowRulesReload", func() {
		reload := func() {
			if err := e.ReloadAllowRules(ctx); err != nil {
				log.Printf("[whitelist] autotrade rules reload: %v", err)
			}
			if err := e.ReloadFlags(ctx); err != nil {
				log.Printf("[flags] reload: %v", err)
			}
			if err := e.ReloadXAssetAllowCombos(ctx); err != nil {
				log.Printf("[whitelist] xasset combos reload: %v", err)
			}
			// D-515 S5：swaparb 表热读——engine 快照+pipeline/dashboard 同源推入（≤60s/5min 兜底生效）。
			if allow, err := e.ReloadSwapArbAllowCombos(ctx); err != nil {
				log.Printf("[whitelist] swaparb combos reload: %v", err)
			} else if pushSwapArb != nil {
				pushSwapArb(allow)
			}
		}
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-kick:
				reload()
			case <-t.C:
				reload()
			}
		}
	})
}

// swapArbAllowFromTable D-515 S5：swaparb_allow_combos 表加载+共享映射（exec.SwapArbSpecsFromRows
// 单源）。加载失败 → fatal（fail-closed：白名单读不出=不开）；零行 → 空表=RUNNING 下
// autoAllow=false 全拒（fail-closed 语义保真，非新闸门）。
func swapArbAllowFromTable(ctx context.Context, st *pgstore.Store) (allow, deny []exec.ComboAllowSpec) {
	allowRows, denyRows, err := st.LoadSwapArbAllowCombos(ctx)
	if err != nil {
		allowRulesFatal("[whitelist] swaparb combos load: %v (startup fail-closed)", err)
	}
	allow, deny = exec.SwapArbSpecsFromRows(allowRows), exec.SwapArbSpecsFromRows(denyRows)
	log.Printf("[whitelist] swaparb combos table: allow=%d deny=%d (env NOX_SWAPARB_*_COMBOS 停读)", len(allow), len(deny))
	return allow, deny
}

// swapArbAllowFromTableOnly allow 轨单取（pipeline/dashboard 注入点——deny 仅 engine 用）。
func swapArbAllowFromTableOnly(ctx context.Context, st *pgstore.Store) []exec.ComboAllowSpec {
	allow, _ := swapArbAllowFromTable(ctx, st)
	return allow
}

// seedSwapArbCombosFromEnv D-515 S5 播种：表空+env 有值 → ParseComboAllow 转行入库一次
// （ON CONFLICT 幂等）；表非空 → 纯读。env 停读后此行即成唯一迁移桥。
func seedSwapArbCombosFromEnv(ctx context.Context, st *pgstore.Store, cfg *config.Config) {
	empty, err := st.SwapArbAllowCombosEmpty(ctx)
	if err != nil {
		allowRulesFatal("[whitelist] swaparb seed check: %v (startup fail-closed)", err)
	}
	if !empty {
		return
	}
	n := 0
	for kind, raw := range map[string]string{"allow": cfg.SwapArbAllowCombosRaw, "deny": cfg.SwapArbDenyCombosRaw} {
		for _, spec := range exec.ParseComboAllow(raw) {
			row := pgstore.SwapArbAllowRow{
				Kind: kind, Canonical: spec.Canonical, VenueLong: spec.VenueLong, VenueShort: spec.VenueShort,
			}
			if spec.MaxLongQty > 0 {
				row.MaxLongQty = &spec.MaxLongQty
			}
			if spec.MaxShortQty > 0 {
				row.MaxShortQty = &spec.MaxShortQty
			}
			if spec.MaxGrossExposureUSD > 0 {
				row.MaxGrossUSD = &spec.MaxGrossExposureUSD
			}
			if err := st.InsertSwapArbAllowCombo(ctx, row); err != nil {
				allowRulesFatal("[whitelist] swaparb seed %s %s: %v", kind, spec.Canonical, err)
			}
			n++
		}
	}
	if n > 0 {
		log.Printf("[whitelist] swaparb combos seeded from env: %d rows (first boot)", n)
	}
}

// WarnXAssetEnvDeprecated D-338 S4：env 单源收口——残值仅告警不消费（migration=唯一源）。
func WarnXAssetEnvDeprecated(cfg *config.Config) {
	if cfg.XAssetAllowCombosRaw != "" {
		log.Printf("[whitelist] deprecated: NOX_XASSET_ALLOW_COMBOS ignored, source=xasset_allow_combos 表（.env/compose 残值请清理）")
	}
}

// xassetAllowFromTable D-338 S3：白名单表加载+映射（两装配点同 slice 同源）。
// 加载失败 → log.Fatal（startup fail-closed：白名单读不出=不开）；零行 → 空 slice
// （合法态=XASSET 全拒，零信任语义不变）。逐行摘要 boot 日志（combo+限额+note 截断）。
func xassetAllowFromTable(ctx context.Context, st *pgstore.Store, environment string) []exec.XAssetComboAllowSpec {
	rows, err := st.LoadXAssetAllowCombos(ctx, environment)
	if err != nil {
		xassetAllowFatal("[whitelist] xasset table load: %v (startup fail-closed)", err)
	}
	// D-403 S3：映射+D-367 剔除规则单点下沉 exec.XAssetSpecsFromRows（engine 热重载同源禁复制）。
	out, dropped := exec.XAssetSpecsFromRows(rows)
	for _, r := range rows {
		if r.NotionalUSD != nil && *r.NotionalUSD <= 0 {
			log.Printf("[whitelist] xasset row DROPPED: %s|%s %s×%s notional_usd=%g <= 0 (fail-closed 坏配置不授权)",
				r.CanonicalBuy, r.CanonicalSell, r.VenueBuy, r.VenueSell, *r.NotionalUSD)
			continue
		}
		notionalNote := "nil"
		if r.NotionalUSD != nil {
			notionalNote = fmt.Sprintf("%g", *r.NotionalUSD)
		}
		log.Printf("[whitelist] xasset row: %s|%s %s×%s long=%g short=%g gross=%g notional=%s note=%.40s",
			r.CanonicalBuy, r.CanonicalSell, r.VenueBuy, r.VenueSell, r.MaxLongQty, r.MaxShortQty, r.MaxGrossUSD, notionalNote, r.Note)
	}
	if dropped > 0 {
		log.Printf("[whitelist] xasset rows dropped=%d (notional_usd<=0 fail-closed, D-367)", dropped)
	}
	if len(out) == 0 {
		log.Printf("[whitelist] xasset table rows=0 env=%s (XASSET 全拒=零信任合法态)", environment)
	} else {
		log.Printf("[whitelist] xasset table rows=%d env=%s", len(out), environment)
	}
	return out
}
