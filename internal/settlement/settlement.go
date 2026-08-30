// Package settlement 结算钩子（D-087 §3.2）。
//
// 每笔平仓有唯一、幂等、可审计的结算动作：strategy_stats / verification_stats / 淘汰统计同源更新。
// Settle 是唯一结算入口，三个调用点（手动平仓 RPC / scanAndClose / reconcile auto_close）统一替换。
package settlement

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/notional"
	"arb/internal/store/pgstore"
	"arb/internal/verification"
)

// Deps 结算依赖（注入面，调用方装配）。
type Deps interface {
	// GetPosition 读单条持仓（含 typed 字段）。
	GetPosition(ctx context.Context, id int64) (*pgstore.Position, error)
	// ListPositionLegs 读持仓腿。
	ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error)
	// ContractSizeByCanonical 查 CS（已含 VenueAccount 归一）。
	ContractSizeByCanonical(ctx context.Context, venue, canonical string) (float64, bool, error)
	// USDPer 查计价币→USD 汇率（FXRateProvider）。
	USDPer(ctx context.Context, quoteCcy string, at time.Time) (float64, bool)
	// UpsertVerificationSample 增量验证统计。
	UpsertVerificationSample(ctx context.Context, vkey string, expectedUsd, realizedUsd float64, closedAt time.Time) error
	// UpdateStrategyStat 策略统计增量。
	UpdateStrategyStat(ctx context.Context, strategy string, realizedPnl float64) error
	// GetStrategyStat 读策略统计（D-154 起仅查 disabled/disabled_reason；累计列不再用于淘汰判定）。
	GetStrategyStat(ctx context.Context, strategy string) (*pgstore.StrategyStat, error)
	// AggregateStrategyStats 实时聚合策略全量平仓净值（D-154 S1 淘汰判定 v2 真相源）。
	AggregateStrategyStats(ctx context.Context, strategy string) (pgstore.AggregateStat, error)
	// DisableStrategy 禁用策略（淘汰）。
	DisableStrategy(ctx context.Context, strategy, reason string) error
	// InsertSettlement 写 position_settlements（幂等 ON CONFLICT DO NOTHING，返修包②返回 won）。
	InsertSettlement(ctx context.Context, s *pgstore.SettlementRow) (won bool, err error)
	// HasSettlement 查 position_settlements 是否已有行（幂等短路）。
	HasSettlement(ctx context.Context, posID int64) (bool, error)
	// SumCashflowsNet 返回持仓 swap+funding+commission 现金流 USD 合计（D-127 P4 返修①：net=realized+cashflowsNet）。
	SumCashflowsNet(ctx context.Context, posID int64) (float64, error)
}

// Eliminator 淘汰判定函数类型（从 autotrade.evaluateElimination 抽出，避免环依赖）。
// D-154 S2 加 medianPnl 入参（小样本判据 median<0 AND avg<0）。
// 返回 (shouldDisable, reason)。
type Eliminator func(totalTrades int, winRate, avgPnl, medianPnl float64, disabled bool) (bool, string)

// Settle 唯一结算入口（D-087 §3.2）。
// 幂等：已有 position_settlements 行 → 直接返回。
// 跳过规则（写行但不入样本）：realized NULL / expectedUsd≤0 / fx 缺失。
// 通过 → UpsertVerificationSample + UpdateStrategyStat + 淘汰检查。
func Settle(ctx context.Context, deps Deps, posID int64, elim Eliminator) error {
	// 幂等短路
	has, err := deps.HasSettlement(ctx, posID)
	if err != nil {
		return fmt.Errorf("settle: check existing: %w", err)
	}
	if has {
		return nil
	}

	// 1. 读 position
	p, err := deps.GetPosition(ctx, posID)
	if err != nil {
		return fmt.Errorf("settle: get position %d: %w", posID, err)
	}
	if p.Status != "CLOSED" {
		return fmt.Errorf("settle: position %d not CLOSED (status=%s)", posID, p.Status)
	}

	// 2. 派生 vkey
	vkey := verification.VkeyFromOppID(p.OpportunityID)

	// 3. 取腿算 notional + holdDays
	legs, err := deps.ListPositionLegs(ctx, posID)
	if err != nil {
		return fmt.Errorf("settle: list legs: %w", err)
	}
	canonical := canonicalFromOppID(p.OpportunityID)
	notionalUsd, fxSkipped := computeNotionalUSD(ctx, deps, legs, canonical)
	holdDays := computeHoldDays(p, legs)

	// 4. 算 expectedUsd（D-087 §3.4 typed 新签名：netBps + swapBpsDaily 按字段取值）
	netBps := p.ExpectedNetBps
	swapBpsDaily := 0.0
	if p.ExpectedSwapBpsDaily != nil {
		swapBpsDaily = *p.ExpectedSwapBpsDaily
	}
	expectedUsd := verification.ExpectedUsd(p.Type, netBps, swapBpsDaily, notionalUsd, holdDays)

	// 5. 跳过规则
	skippedReason := ""
	switch {
	case p.RealizedPnl == nil:
		skippedReason = "realized_pnl is NULL"
	case fxSkipped:
		skippedReason = "fx: quote ccy unavailable"
	case expectedUsd <= 0:
		skippedReason = "expected_usd <= 0"
	case strings.Contains(p.Note, "pnl_estimated"): // D-159 W3a R4：estimated 不入正式统计
		skippedReason = "pnl_estimated: partial or no calibration"
	}

	// 6. 写 position_settlements（先写防并发，ON CONFLICT DO NOTHING）
	// 返修包②：InsertSettlement 返回 won——输家（并发对手已写入）直接 return nil，不走步骤 7/8 防双计。
	// 附带小项：skipped 行 notional_usd 写 NULL（0 是伪造值）。
	row := &pgstore.SettlementRow{
		PositionID:    posID,
		Vkey:          vkey,
		ExpectedUsd:   expectedUsd,
		HoldDays:      holdDays,
		SkippedReason: skippedReason,
		SettledAt:     time.Now(),
	}
	if skippedReason == "" {
		row.NotionalUsd = &notionalUsd // 非跳过行写真值
	} // 跳过行 NotionalUsd=nil → INSERT 写 NULL
	if p.RealizedPnl != nil {
		row.RealizedUsd = *p.RealizedPnl
	}
	won, err := deps.InsertSettlement(ctx, row)
	if err != nil {
		return fmt.Errorf("settle: insert settlement: %w", err)
	}
	if !won {
		log.Printf("[settlement] pos=%d concurrent loser (settlement already exists), skipping stat update", posID)
		return nil
	}

	// 7. 跳过=不入样本不入统计，直接返回
	if skippedReason != "" {
		log.Printf("[settlement] pos=%d skipped: %s", posID, skippedReason)
		return nil
	}

	// 8. 通过 → UpsertVerificationSample + UpdateStrategyStat + 淘汰
	// D-127 P4 返修①：net = realized + cashflowsNet（swap+funding−commission 实收纳入淘汰/复盘口径，防套息策略被误杀）。
	// D-159 W3a R4 双计拆除后语义：realized_pnl = 纯价格差 PnL（calcLegPnL/calibratePnL 已去 swap/commission），
	// cashflowsNet = swap+funding+commission 组件 → netPnl = 净值一次合成，无双计。
	cashflowsNet, err := deps.SumCashflowsNet(ctx, posID)
	if err != nil {
		log.Printf("[settlement] sum cashflows net pos=%d: %v (fallback net=realized)", posID, err)
		cashflowsNet = 0 // 查询失败回退到旧口径（不阻塞结算）
	}
	netPnl := *p.RealizedPnl + cashflowsNet
	// D-159 W3a R4 影子对拍框架：S2 后 realized_pnl 是新口径（纯价格差），
	// netPnl = realized_pnl + cashflowsNet = 净值一次合成。
	// 旧口径（双计）独立计算待对拍期补（决策方另发指令），本包只标记框架就绪。
	log.Printf("[settlement] SHADOW pos=%d netPnl=%.2f realized=%.2f cashflowsNet=%.2f (shadow framework ready, legacy calc pending)",
		posID, netPnl, *p.RealizedPnl, cashflowsNet)
	closedAt := time.Now()
	if p.ClosedAt != nil {
		closedAt = *p.ClosedAt
	}
	if err := deps.UpsertVerificationSample(ctx, vkey, expectedUsd, netPnl, closedAt); err != nil {
		// D-159 W2a R3-9：settlement 失败必须进入可重试状态（不只打日志后宣告完成）。
		return fmt.Errorf("settle: upsert verification pos=%d: %w", posID, err)
	}
	if err := deps.UpdateStrategyStat(ctx, p.Type, netPnl); err != nil {
		return fmt.Errorf("settle: update strategy stat pos=%d: %w", posID, err)
	}

	// 淘汰检查（D-154 S1：输入源从 strategy_stats 累计列改为 AggregateStrategyStats 实时聚合；
	// disabled flag 仍查 strategy_stats——启停载体不变。两调用点同源 P3。）
	if elim != nil {
		stat, err := deps.GetStrategyStat(ctx, p.Type)
		if err != nil {
			log.Printf("[settlement] get strategy stat: %v", err)
			return nil
		}
		agg, err := deps.AggregateStrategyStats(ctx, p.Type)
		if err != nil {
			log.Printf("[settlement] aggregate strategy stats: %v", err)
			return nil
		}
		winRate := 0.0
		if agg.Total > 0 {
			winRate = float64(agg.Wins) / float64(agg.Total)
		}
		disable, reason := elim(agg.Total, winRate, agg.AvgPnl, agg.MedianPnl, stat.Disabled)
		if disable {
			if err := deps.DisableStrategy(ctx, p.Type, reason); err != nil {
				log.Printf("[settlement] disable strategy %s: %v", p.Type, err)
			} else {
				log.Printf("[settlement] STRATEGY DISABLED type=%s: %s", p.Type, reason)
			}
		}
	}
	return nil
}

// computeNotionalUSD Σ OPEN FILLED 腿 LegUSD（CS 经归一 + fx 经 USDPer）。
// 返回 (notionalUSD, fxSkipped)：任一腿 fx 不可得 → fxSkipped=true（notional 可能部分算）。
func computeNotionalUSD(ctx context.Context, deps Deps, legs []pgstore.PositionLeg, canonical string) (float64, bool) {
	quoteCcy := notional.QuoteCcy(canonical)
	total := 0.0
	skipped := false
	for _, l := range legs {
		if l.Phase != "OPEN" || l.Status != "FILLED" {
			continue
		}
		if l.PriceActual == nil || *l.PriceActual <= 0 || l.Qty <= 0 {
			continue
		}
		cs := 1.0 // CEX 腿 CS=1
		if c, ok, _ := deps.ContractSizeByCanonical(ctx, l.Venue, canonical); ok && c > 0 {
			cs = c
		}
		fx := 1.0
		if quoteCcy != "" && quoteCcy != "USD" {
			rate, ok := deps.USDPer(ctx, quoteCcy, l.Ts)
			if !ok {
				skipped = true
				continue
			}
			fx = rate // USDPer 返回 USD per quoteCcy，直接作 fx（D-087 返修③：删双重求逆）
		}
		total += notional.LegUSD(l.Qty, cs, *l.PriceActual, fx)
	}
	return total, skipped
}

// computeHoldDays (closed_at − opened_at)/24h；opened_at NULL → min(OPEN FILLED 腿 ts)。
func computeHoldDays(p *pgstore.Position, legs []pgstore.PositionLeg) float64 {
	if p.ClosedAt == nil {
		return 0
	}
	openedAt := p.OpenedAt
	if openedAt == nil {
		var minTs time.Time
		for _, l := range legs {
			if l.Phase == "OPEN" && l.Status == "FILLED" {
				if minTs.IsZero() || l.Ts.Before(minTs) {
					minTs = l.Ts
				}
			}
		}
		if !minTs.IsZero() {
			openedAt = &minTs
		}
	}
	if openedAt == nil {
		return 0
	}
	return p.ClosedAt.Sub(*openedAt).Hours() / 24
}

// canonicalFromOppID 从 opportunity_id 解析 canonical（格式 PREFIX:SYMBOL:...）。
// 与 verification/pgstore 同源纯函数，本包不依赖 autotrade（避免环依赖）。
func canonicalFromOppID(oppID string) string {
	// 剥离旧 tick 尾时间戳
	ck := oppID
	if idx := lastIndexColon(oppID); idx >= 0 {
		tail := oppID[idx+1:]
		if isAllDigits(tail) {
			ck = oppID[:idx]
		}
	}
	parts := splitColon(ck)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

func lastIndexColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func splitColon(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}
