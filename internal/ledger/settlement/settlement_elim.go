package settlement

// 淘汰检查（D-261 S5 从 settlement.go SettleAt 内拆出，纯搬家提取为函数）。

import (
	"context"
	"fmt"
	"log"

	"nox/internal/review/mmm"
	"nox/internal/store/pgstore"
)

// runElimination 淘汰检查（D-261 S5 从 SettleAt 拆出；R4 复原被裁的决策理由四行）。
// 输入源从 strategy_stats 累计列改为 AggregateStrategyStats 实时聚合；
// disabled flag 仍查 strategy_stats——启停载体不变。两调用点同源 P3。
// 聚合改 ByEnvironment，按持仓自身 p.Environment 过滤——每个 environment 独立淘汰判定。
// family 非 nil 时改用族级聚合（CARRY/SWAPARB 合并样本，避免分桶样本量不足）。
// 输入源从 strategy_stats 累计列改为 AggregateStrategyStats 实时聚合；
// disabled flag 仍查 strategy_stats——启停载体不变。两调用点同源 P3。
// 聚合改 ByEnvironment，按持仓自身 p.Environment 过滤——每个 environment 独立淘汰判定。
// family 非 nil 时改用族级聚合（CARRY/SWAPARB 合并样本，避免分桶样本量不足）。
func runElimination(ctx context.Context, deps Deps, p *pgstore.Position, elim Eliminator, family FamilyResolver) {
	if elim == nil {
		return
	}
	stat, err := deps.GetStrategyStat(ctx, p.Type)
	if err != nil {
		log.Printf("[settlement] get strategy stat: %v", err)
		return
	}
	var agg pgstore.AggregateStat
	if family != nil {
		famStrategies := family(p.Type)
		agg, err = deps.AggregateStrategyStatsByEnvironmentFamily(ctx, famStrategies, p.Environment)
		if err != nil {
			log.Printf("[settlement] aggregate strategy stats family: %v", err)
			return
		}
	} else {
		agg, err = deps.AggregateStrategyStatsByEnvironment(ctx, p.Type, p.Environment)
		if err != nil {
			log.Printf("[settlement] aggregate strategy stats: %v", err)
			return
		}
	}
	winRate := 0.0
	if agg.Total > 0 {
		winRate = float64(agg.Wins) / float64(agg.Total)
	}
	disable, reason := elim(ctx, agg.Total, winRate, agg.AvgPnl, agg.MedianPnl, stat.Disabled)
	if disable {
		reason += elimEvidenceSuffix(ctx, deps, agg.Total, winRate)
		// 族内全禁——family 非 nil 时禁用族内全部策略（CARRY/SWAPARB 同族），
		// 避免复活场景下 CARRY 触发淘汰后 SWAPARB 存活至重启/自身结算（D-256 循环亏漏洞残留）。
		famStrategies := []string{p.Type}
		if family != nil {
			famStrategies = family(p.Type)
		}
		for _, fam := range famStrategies {
			if err := deps.DisableStrategy(ctx, fam, reason); err != nil {
				log.Printf("[settlement] disable strategy %s: %v", fam, err)
			}
		}
		log.Printf("[settlement] STRATEGY DISABLED family=%v: %s", famStrategies, reason)
		_ = deps.InsertAlert(ctx, nil, "warn", fmt.Sprintf("[策略淘汰] %v: %s", famStrategies, reason))
	}
}

// elimEvidenceSuffix D-180 S①：判死 reason 追加 B.12 证据字段（与 autotrade 同式）。
// Zσ 走 app_config NOX_ELIM_ZSIGMA 热读（默认 2）；Deps 不实现/读败回落默认。
func elimEvidenceSuffix(ctx context.Context, deps Deps, total int, winRate float64) string {
	z := 2.0
	if v, ok, err := deps.AppConfigFloat(ctx, "NOX_ELIM_ZSIGMA"); err == nil && ok && v > 0 {
		z = v
	}
	return mmm.ElimEvidence(total, winRate, 0.30, z)
}
