// D-211 S5：从 service_hedge.go 拆出 carry 计算与待领取奖励（原文件 304 行超 300 软限）。
package dashboard

import (
	"context"
	"time"

	v1 "arb/internal/gen/dashboardv1"
)

// computeHedgeCarry 计算 carry 统计（近 7 日 + 累计 + 年化）。
// cum_usd = 全量 (energy+voting)×price + interest（price 用当期 mid 近似——历史日 price 快照在 daily_income.price）；
// annualized_pct = cum_usd / (qty×mid) × 365（年化分母=现货市值，A1）。
func (s *Service) computeHedgeCarry(ctx context.Context, mid, qtyStaked float64) *v1.HedgeCarry {
	now := time.Now().UTC()
	from7d := now.Add(-7 * 24 * time.Hour)

	// 近 7 日 energy/voting（全源聚合）
	energy7d, voting7d, _, err := s.store.SumDailyIncomeByRange(ctx, &from7d, &now)
	if err != nil {
		energy7d, voting7d = 0, 0
	}
	// 近 7 日隔夜息
	overnight7d, err := s.store.SumCMCOvernightByRange(ctx, &from7d, &now)
	if err != nil {
		overnight7d = 0
	}
	// 累计全量
	energyAll, votingAll, interestAll, err := s.store.SumDailyIncomeByRange(ctx, nil, nil)
	if err != nil {
		energyAll, votingAll, interestAll = 0, 0, 0
	}
	cumUsd := (energyAll+votingAll)*mid + interestAll

	// 年化（D-236 变更14 时间感知：分母=现货市值，分子按经过天数折算——
	// 旧式 cum×365 把累计当单日收益，累计增大后荒谬值显形；firstIncomeDay 缺失=诚实 0→前端「—」）。
	var annualized float64
	if mid > 0 && qtyStaked > 0 {
		firstDay, err := s.store.FirstDailyIncomeDay(ctx)
		if err == nil && firstDay != nil {
			days := now.Sub(*firstDay).Hours() / 24
			if days < 1 {
				days = 1
			}
			annualized = cumUsd / (qtyStaked * mid) / days * 365 * 100 // 百分数
		}
	}

	// D-236 变更15：对冲后分段（窗口=[hedgeStart,now]，hedgeStart=fin_cmc_positions 最早 opened_at）。
	// 空表（未建对冲）→ 对冲后视图回落=结构累计同值并注明（设计规格）。
	hedgedCumUsd, hedgedAnnual := cumUsd, annualized
	if hedgeStart, err := s.store.FirstCMCPositionOpenedAt(ctx); err == nil && hedgeStart != nil {
		energyH, votingH, interestH, err := s.store.SumDailyIncomeByRange(ctx, hedgeStart, &now)
		if err == nil {
			hedgedCumUsd = (energyH+votingH)*mid + interestH
			daysH := now.Sub(*hedgeStart).Hours() / 24
			if daysH < 1 {
				daysH = 1
			}
			if mid > 0 && qtyStaked > 0 {
				hedgedAnnual = hedgedCumUsd / (qtyStaked * mid) / daysH * 365 * 100
			}
		}
	}

	// D-260 S2：月至今净额（窗口=[月初 UTC,now]，与 cum 同口径，mid 同 snap；空月 → 0+sample 0 前端「—」）。
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	mtdEnergy, mtdVoting, mtdInterest, err := s.store.SumDailyIncomeByRange(ctx, &monthStart, &now)
	if err != nil {
		mtdEnergy, mtdVoting, mtdInterest = 0, 0, 0
	}
	mtdSampleDays, _ := s.store.CountDailyIncomeByRange(ctx, &monthStart, &now)
	mtdNetUsd := (mtdEnergy+mtdVoting)*mid + mtdInterest
	return &v1.HedgeCarry{
		TrxRental_7DTrx:     energy7d,
		TrxVote_7DTrx:       voting7d,
		CmcOvernight_7DUsd:  overnight7d,
		CumUsd:              cumUsd,
		AnnualizedPct:       annualized,
		HedgedCumUsd:        hedgedCumUsd,
		HedgedAnnualizedPct: hedgedAnnual,
		MtdNetUsd:           mtdNetUsd,
		MtdSampleDays:       int32(mtdSampleDays),
	}
}
