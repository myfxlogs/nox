// paper_spot_carry.go D-350 阶段②：spot-carry 纸面验证轨步进（开仓/累积/退出，零执行风险）。
// 记账口径：accrued += sideFactor×last_funding_ann/100/365×间隔天×qty（雷达快照费率，
// 诚实声明非逐结算值）；short_perp 收正 funding（sideFactor=+1），long_perp 收负 funding
// （sideFactor=−1，funding 为负 → 收入为正）。
package scan

import (
	"context"
	"fmt"
	"math"
	"time"

	"arb/internal/store/pgstore"
)

// 纸面轨参数（D-350 阶段②任务书定值）。
const (
	// PaperOpenNetPct 纸面开仓线：net_spot_carry ≥ 20%/yr。
	PaperOpenNetPct = 20.0
	// PaperQtyUSD 虚拟仓位（固定 $1000，不复利）。
	PaperQtyUSD = 1000.0
	// paperExitNormalizedPct 退出线：|last_funding_ann| < 10%/yr。
	paperExitNormalizedPct = 10.0
	// paperExitStreakRounds 连续低于退出线的轮数。
	paperExitStreakRounds = 2
	// paperMaxAgeDays 满龄强评（天）。
	paperMaxAgeDays = 7.0
)

// PaperStore D-350 阶段②：纸面轨存储依赖（*pgstore.Store 原生实现）。
type PaperStore interface {
	ListCexUniverse(ctx context.Context) (map[string]pgstore.CexUniverseRow, error)
	ListActivePaperSpotCarry(ctx context.Context) ([]pgstore.PaperSpotCarry, error)
	InsertPaperSpotCarry(ctx context.Context, r *pgstore.PaperSpotCarry) error
	AccruePaperSpotCarry(ctx context.Context, id int64, lastFundingAnn, accruedAdd float64, lowStreak int) error
	ExitPaperSpotCarry(ctx context.Context, id int64, reason string) error
}

// StepPaperSpotCarry 每轮扫描后调用：①开仓（候选 net≥20 且持续性闸已过且无同 symbol 活跃行）
// ②逐仓累积 ③退出（|funding|<10% 连续 2 轮 → normalized；满 7 天 → age_7d 强评）。
// 返回本轮开仓/退出行数。
func StepPaperSpotCarry(ctx context.Context, st PaperStore, now time.Time) (opened, exited int, err error) {
	uni, err := st.ListCexUniverse(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("paper step: list cex universe: %w", err)
	}
	active, err := st.ListActivePaperSpotCarry(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("paper step: list active: %w", err)
	}
	activeByKey := make(map[string]struct{}, len(active))
	for _, p := range active {
		activeByKey[p.Venue+"|"+p.Symbol] = struct{}{}
	}
	// ①开仓
	for key, u := range uni {
		if u.Status != "TRADING" || (u.Tier != "T1" && u.Tier != "T3") || u.FundingAnn == nil {
			continue
		}
		if now.Sub(u.FirstSeen) < spotCarryMinAge {
			continue
		}
		if abs(*u.FundingAnn)-SPOT_CARRY_COST_PCT < PaperOpenNetPct {
			continue
		}
		if _, ok := activeByKey[key]; ok {
			continue
		}
		side := "short_perp"
		if *u.FundingAnn < 0 {
			side = "long_perp"
		}
		r := &pgstore.PaperSpotCarry{
			Venue: u.Venue, Symbol: u.Symbol, Side: side,
			EntryFundingAnn: *u.FundingAnn, QtyUSD: PaperQtyUSD,
		}
		if err := st.InsertPaperSpotCarry(ctx, r); err != nil {
			if err == pgstore.ErrPaperExists {
				continue
			}
			return opened, exited, fmt.Errorf("paper step: open %s: %w", key, err)
		}
		opened++
	}
	// ②累积 + ③退出
	for _, p := range active {
		u, ok := uni[p.Venue+"|"+p.Symbol]
		if !ok || u.FundingAnn == nil {
			continue // 雷达本轮缺行（停更/摘牌瞬窗）→ 跳过不误退
		}
		f := *u.FundingAnn
		base := p.EntryTs
		if p.LastAccrueTs != nil {
			base = *p.LastAccrueTs
		}
		days := now.Sub(base).Hours() / 24
		if days < 0 {
			days = 0
		}
		sideFactor := 1.0
		if p.Side == "long_perp" {
			sideFactor = -1.0
		}
		accruedAdd := sideFactor * f / 100 / 365 * days * p.QtyUSD
		streak := p.LowStreak
		if math.Abs(f) < paperExitNormalizedPct {
			streak++
		} else {
			streak = 0
		}
		if streak >= paperExitStreakRounds {
			if err := st.ExitPaperSpotCarry(ctx, p.ID, "normalized"); err != nil {
				return opened, exited, fmt.Errorf("paper step: exit %d: %w", p.ID, err)
			}
			exited++
			continue
		}
		if now.Sub(p.EntryTs).Hours()/24 >= paperMaxAgeDays {
			if err := st.ExitPaperSpotCarry(ctx, p.ID, "age_7d"); err != nil {
				return opened, exited, fmt.Errorf("paper step: exit age %d: %w", p.ID, err)
			}
			exited++
			continue
		}
		if err := st.AccruePaperSpotCarry(ctx, p.ID, f, accruedAdd, streak); err != nil {
			return opened, exited, fmt.Errorf("paper step: accrue %d: %w", p.ID, err)
		}
	}
	return opened, exited, nil
}
