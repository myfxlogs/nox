// Package review 复盘闭环（§13.6，D-034）：平仓后统计复盘，deviation_bps 归因
// + 聚合调参建议，回流 knowledge_entries 待人工复核。
package review

import (
	"context"
	"fmt"
	"log"
	"math"

	"arb/internal/store/pgstore"
)

// agg 按类型聚合的偏差统计。
type agg struct {
	n           int
	devBpsSum   float64
	devBps2Sum  float64
	holdingDays float64 // 息差型累计持有天数（年化折算分母）
}

// Store 复盘引擎依赖的最小存储接口（生产 = *pgstore.Store，测试 = fake）。
type Store interface {
	ListPositions(ctx context.Context, status string, limit, offset int) ([]pgstore.Position, error)
	ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error)
	ListCashflows(ctx context.Context, posID int64) ([]pgstore.Cashflow, error)
	UpsertKnowledgeEntry(ctx context.Context, e *pgstore.KnowledgeEntry) (int64, error)
	// notionalFromLegs 需按 (venueKey, broker_symbol) 查 contract_size。
	ContractSizeByBrokerSymbol(ctx context.Context, venueKey, brokerSymbol string) (float64, bool, error)
}

// Config 复盘阈值（D-035：全部经 config 注入，禁止散落硬编码）。
type Config struct {
	MinSamples         int     // 聚合出调参建议的最小样本数（防单笔噪声）
	CrossBiasBps       float64 // 价差型平均偏差超此值才发建议（bps）
	CarryBiasAnnualPct float64 // 息差型平均偏差超此值才发建议（年化百分点）
}

// Run 对全部已平仓（CLOSED）持仓执行统计复盘（幂等：按 signature upsert）。
// 每笔写 review:pos:{id}；某类型样本 ≥ MinSamples 时写 review:summary:{type} 调参建议。
// 非阻塞语义由调用方（fire-and-forget goroutine）保证。
func Run(ctx context.Context, st Store, cfg Config) error {
	// 全量拉已平仓持仓（分页循环，防单页截断）
	const page = 200
	var all []pgstore.Position
	for offset := 0; ; offset += page {
		pos, err := st.ListPositions(ctx, "CLOSED", page, offset)
		if err != nil {
			return fmt.Errorf("list closed positions: %w", err)
		}
		all = append(all, pos...)
		if len(pos) < page {
			break
		}
	}
	if len(all) == 0 {
		return nil
	}

	byType := map[string]*agg{}

	for i := range all {
		p := &all[i]
		if p.RealizedPnl == nil || p.OpenedAt == nil || p.ClosedAt == nil {
			continue // 缺回填数据不归因
		}
		legs, err := st.ListPositionLegs(ctx, p.ID)
		if err != nil {
			return fmt.Errorf("legs of pos %d: %w", p.ID, err)
		}
		cashflows, err := st.ListCashflows(ctx, p.ID)
		if err != nil {
			return fmt.Errorf("cashflows of pos %d: %w", p.ID, err)
		}

		notional, buySymbol, buyVenue := notionalFromLegs(legs, func(vk, bs string) float64 {
			cs, ok, _ := st.ContractSizeByBrokerSymbol(ctx, vk, bs)
			if !ok {
				return 0
			}
			return cs
		})
		if notional <= 0 {
			continue // 无买腿或 qty=0，无法算名义额
		}
		realized := *p.RealizedPnl
		for _, c := range cashflows {
			realized += c.AmountUSD
		}

		holdingDays := p.ClosedAt.Sub(*p.OpenedAt).Hours() / 24
		expectedHoldingBps := expectedHoldingBps(p, holdingDays)
		deviationBps := realized/notional*1e4 - expectedHoldingBps

		// 每笔回填 knowledge_entries
		verdict := "spot_on"
		switch {
		case deviationBps > 0.1:
			verdict = "better_than_expected"
		case deviationBps < -0.1:
			verdict = "worse_than_expected"
		}
		rationale := fmt.Sprintf("deviation_bps=%.2f (realized=%.4f, notional=%.2f, expected_holding_bps=%.2f, holding_days=%.2f)",
			deviationBps, realized, notional, expectedHoldingBps, holdingDays)
		if _, err := st.UpsertKnowledgeEntry(ctx, &pgstore.KnowledgeEntry{
			Signature: fmt.Sprintf("review:pos:%d", p.ID),
			VenueKeys: []string{buyVenue},
			Symbol:    buySymbol,
			Verdict:   verdict,
			Rationale: rationale,
			Source:    "review",
		}); err != nil {
			return fmt.Errorf("upsert review pos %d: %w", p.ID, err)
		}

		a := byType[p.Type]
		if a == nil {
			a = &agg{}
			byType[p.Type] = a
		}
		a.n++
		a.devBpsSum += deviationBps
		a.devBps2Sum += deviationBps * deviationBps
		a.holdingDays += holdingDays
	}

	// 聚合调参建议
	for typ, a := range byType {
		if a.n < cfg.MinSamples {
			continue
		}
		mean := a.devBpsSum / float64(a.n)
		var sd float64
		if a.n > 1 {
			v := (a.devBps2Sum - a.devBpsSum*a.devBpsSum/float64(a.n)) / float64(a.n-1)
			if v > 0 {
				sd = math.Sqrt(v)
			}
		}
		suggestion := tuningSuggestion(typ, a, mean, cfg)
		if suggestion == "" {
			continue
		}
		if _, err := st.UpsertKnowledgeEntry(ctx, &pgstore.KnowledgeEntry{
			Signature: fmt.Sprintf("review:summary:%s", typ),
			Verdict:   "tuning",
			Rationale: suggestion,
			Source:    "review",
		}); err != nil {
			return fmt.Errorf("upsert review summary %s: %w", typ, err)
		}
		log.Printf("[review] %s 样本=%d 平均deviation=%.2f bps σ=%.2f → %s", typ, a.n, mean, sd, suggestion)
	}
	return nil
}

// notionalFromLegs 从买腿计算名义额（positions 无 notional 列，§2.11）。
// 名义额 = 买腿 qty × price × contract_size（price_actual 优先，回退 price_expected）。
// 乘 contract_size——旧版漏乘致 review deviation 被放大 1000 倍（BRENT cs=1000）。
// csFn 按 (venueKey, broker_symbol) 查 CS；未命中回退 1（CEX 无 CS 概念）。
func notionalFromLegs(legs []pgstore.PositionLeg, csFn func(venueKey, brokerSymbol string) float64) (notional float64, symbol, venue string) {
	for i := range legs {
		l := &legs[i]
		if l.Side != "BUY" {
			continue
		}
		price := l.PriceExpected
		if l.PriceActual != nil {
			price = *l.PriceActual
		}
		cs := csFn(l.Venue, l.Symbol)
		if cs <= 0 {
			cs = 1 // 安全回退（CEX 无 CS 概念，或未映射）
		}
		if symbol == "" {
			symbol = l.Symbol
			venue = l.Venue
		}
		notional += l.EffectiveQty() * price * cs // D-311 R6：成交口径（CEX=基础币）
	}
	return notional, symbol, venue
}

// expectedHoldingBps 按类型折算 expected_holding_bps（§13.7 口径分型，D-028）：
// 价差型（CROSS_VENUE）= expected_net_bps 快照（一次性，直接可比）；
// 息差型（FUNDING_HEDGE/CARRY）= expected_net_bps × 持有天数/365（年化→持有期）。
func expectedHoldingBps(p *pgstore.Position, holdingDays float64) float64 {
	switch p.Type {
	case "CROSS_VENUE":
		return p.ExpectedNetBps
	default: // FUNDING_HEDGE / CARRY：年化净收益按持有期折算
		return p.ExpectedNetBps * holdingDays / 365
	}
}

// tuningSuggestion 按类型产出调参建议；无建议返回空串。
func tuningSuggestion(typ string, a *agg, mean float64, cfg Config) string {
	if a.holdingDays <= 0 {
		return ""
	}
	// 息差型：平均偏差按持有期年化回退为年化百分点
	annualPct := mean / 1e4 * (365 * float64(a.n) / a.holdingDays) * 100
	switch {
	case typ == "CROSS_VENUE" && math.Abs(mean) > cfg.CrossBiasBps:
		if mean > 0 {
			return fmt.Sprintf("CROSS_VENUE 平均实收高于预期 %.2f bps（样本 %d）→ 成本模型偏保守，可上调检测阈值", mean, a.n)
		}
		return fmt.Sprintf("CROSS_VENUE 平均实收低于预期 %.2f bps（样本 %d）→ 存在未计入成本/滑点，需校正成本模型", -mean, a.n)
	case typ != "CROSS_VENUE" && math.Abs(annualPct) > cfg.CarryBiasAnnualPct:
		if annualPct > 0 {
			return fmt.Sprintf("%s 平均实收年化高于预期 %.2f%%（样本 %d）→ 息差被低估，可上调门槛", typ, annualPct, a.n)
		}
		return fmt.Sprintf("%s 平均实收年化低于预期 %.2f%%（样本 %d）→ 息差被高估，需校正 funding/swap 数据或成本模型", typ, -annualPct, a.n)
	}
	return ""
}
