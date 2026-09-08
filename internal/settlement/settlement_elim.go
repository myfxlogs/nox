package settlement

// 淘汰检查（D-261 S5 从 settlement.go SettleAt 内拆出，纯搬家提取为函数）。

import (
	"context"
	"log"

	"arb/internal/store/pgstore"
)

// runElimination D-154 S1/D-200/D-256-A S2：淘汰检查（从 SettleAt 提取）。
// 输入源从 strategy_stats 累计列改为 AggregateStrategyStats 实时聚合；
// disabled flag 仍查 strategy_stats——启停载体不变。两调用点同源 P3。
// D-200：聚合改 ByEnvironment，按持仓自身 p.Environment 过滤——每个 environment 独立淘汰判定。
// D-256-A S2：family 非 nil 时改用族级聚合（CARRY/SWAPARB 合并样本，避免分桶样本量不足）。
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
	disable, reason := elim(agg.Total, winRate, agg.AvgPnl, agg.MedianPnl, stat.Disabled)
	if disable {
		// D-256-A R1：族内全禁——family 非 nil 时禁用族内全部策略（CARRY/SWAPARB 同族），
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
	}
}
