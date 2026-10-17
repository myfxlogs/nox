// engine_xasset_reload.go D-403 S3：XASSET 白名单热重载——approve 写表后免重启生效。
// 读点 xassetSpecs（atomic Load）vs 写点 Reload（atomic Store）竞态安全，S6 race ×3 强制。
package autotrade

import (
	"context"
	"fmt"
	"log"

	"nox/internal/econkb"
	"nox/internal/trading/exec"
)

// xassetSpecs 白名单读侧：热重载指针优先；nil（零值 Engine 直构的测试路径）回退 cfg
// 启动加载值——两态语义一致（都是「当前已加载的白名单全集」）。
func (e *Engine) xassetSpecs() []exec.XAssetComboAllowSpec {
	if p := e.xassetAllow.Load(); p != nil {
		return *p
	}
	return e.cfg.XAssetAllowCombos
}

// ReloadXAssetAllowCombos D-403 S3：重跑 LoadXAssetAllowCombos+共享映射
// （exec.XAssetSpecsFromRows 单源，D-367 剔除规则随源），atomic 指针整体替换。
// XAssetReloader 最小接口的实现侧（dashboard→autotrade 接口注入防循环依赖，D-195-A 先例）。
func (e *Engine) ReloadXAssetAllowCombos(ctx context.Context) error {
	// D-584 venue 退役守卫：armed 评估环每拍——venue 双簿消失组合自动
	// enabled=false+note 留痕（best-effort 不阻塞重载）。
	// D-587④ 坑签名闸评估环：armed enabled 组合命中坑签名且无 D# 豁免→自动摘。
	if n, perr := e.store.DisablePitHitCombos(ctx, econkb.Dir()); perr == nil && n > 0 {
		log.Printf("[xasset] 坑签名闸：%d armed 组合命中坑→自动摘（豁免须 pit_exempt_d 落 D#）", n)
	}
	// kill 扳机：判据落表组合净 PnL/连败超限→自动摘+平仓（淘汰数学化）。
	if n, kerr := e.store.DisableKillTriggeredCombos(ctx); kerr == nil && n > 0 {
		log.Printf("[xasset] kill 扳机：%d armed 组合超限→自动摘+平仓联动（判据落表非硬编码）", n)
	}
	if n, err := e.store.RetireCombosMissingVenue(ctx); err == nil && n > 0 {
		log.Printf("[whitelist] venue 退役: %d 组合自动摘牌（broker_accounts+exchange_configs 双簿消失）", n)
	}
	// D-599 armed 平仓腿：被摘/被坑组合若在持仓 → 双腿同步平仓触发
	// （退出动作不落=disabled 组合持仓留在持仓位裸跑是禁线坑）。
	if e.ClosePositionFn != nil {
		if pids, err := e.store.DisabledCombosOpenPositions(ctx); err == nil {
			for _, pid := range pids {
				if cerr := e.ClosePositionFn(ctx, pid); cerr != nil {
					log.Printf("[xasset-close] 平仓 pos#%d 败: %v（处置轨兜底）", pid, cerr)
				}
			}
		}
	}
	rows, err := e.store.LoadXAssetAllowCombos(ctx, e.cfg.Environment)
	if err != nil {
		return fmt.Errorf("reload xasset allow combos: %w", err)
	}
	specs, dropped := exec.XAssetSpecsFromRows(rows)
	e.xassetAllow.Store(&specs)
	log.Printf("[whitelist] xasset reload: rows=%d dropped=%d (D-403 热重载免重启)", len(specs), dropped)
	return nil
}

// XAssetAllowMatch D-017 R1：发现层白名单前置滤读口——直读本 Engine 热重载 holder
// （xassetSpecs，ReloadXAssetAllowCombos/configKick 即时生效，无第二加载器无 TTL 缓存；
// 与执行闸 handleOpportunity 同一数据源=零语义漂移）。detector Allow 经 cmd/core
// 装配延迟绑定到此方法。
func (e *Engine) XAssetAllowMatch(canonical, canonicalSell, venueBuy, venueSell string) bool {
	return exec.MatchXAssetComboAllow(e.xassetSpecs(), canonical, canonicalSell, venueBuy, venueSell) != nil
}
