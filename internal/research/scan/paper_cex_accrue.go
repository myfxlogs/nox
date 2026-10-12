// paper_cex_accrue.go —— D-555 S2：双CEX 纸面轨累积+退出段（S1 开仓生产的 ②③ 段）。
// 结息=perp 腿 funding 实付模拟——AccruePaperLegs 复用（cfd 轨同件双腿结算对齐，
// 本轨单 funding 腿、sideFactor=+1 收息）；basis 每轮随候选更 last（候选缺席=NULL
// 诚实缺）；退出四因沿 spot 轨语义：low_streak 连续→normalized、funding 翻负→adverse、
// 候选连续缺席→data_lost、满龄→age_7d（毕业合格集天然生效）。
package scan

import (
	"context"
	"fmt"
	"math"
	"time"

	"nox/internal/data/fact"
	"nox/internal/data/keydomain"
	"nox/internal/store/pgstore"
)

// accrueExitCexPaper 双CEX 行累积与退出（返回本轮退出数+率源缺跳过数）。
// candByKey=本轮 FUNDING_HEDGE 候选索引（key=spotKey×perpKey|canonical）；
// accrable=active+demo 行全集（本轨行 structure=long_spot_short_perp）。
// R1③：率源缺计数随返回值归轮聚合日志（不再逐轮独立 warn——「opened=N skips={…}」
// 单行口径下 accrue_skip 并入同键，防缺 facts 恒真时双倍行频冲 ≤2行/分 验收）。
func accrueExitCexPaper(ctx context.Context, st PaperStore, accrable []pgstore.PaperSpotCarry,
	candByKey map[string]CexPaperCand, now time.Time) (int, int, error) {
	exited := 0
	noFactWarn := 0
	for _, p := range accrable {
		if p.Structure != "long_spot_short_perp" {
			continue // 仅本轨行（spot 轨/cfd 轨/mtm 轨各行归其步进）
		}
		// 候选键=开仓同键（typed keys；cexRowKey 同构）。
		key := candKeyOf(p)
		c, hasCand := candByKey[key]
		base := p.EntryTs
		if p.LastAccrueTs != nil {
			base = *p.LastAccrueTs
		}
		// last_basis：候选在=perp−spot bps（qbus 同源，与 entry 同公式）；缺=NULL 诚实缺。
		var lastBasis *float64
		if hasCand && c.SpotAsk > 0 && c.PerpBid > 0 {
			bps := (c.PerpBid - c.SpotAsk) / c.SpotAsk * 1e4
			lastBasis = &bps
		}
		if !hasCand {
			if p.Status != "active" {
				continue // demo 行缺席：无数据不误记
			}
			// 候选缺席=本轮无 funding/报价面（数据缺席）：缺席不计时不 streak；
			// 连续缺席 ≥N 轮 → data_lost（同 D-474a S4 语义）。
			miss := p.MissStreak + 1
			if miss >= paperMissExitRounds {
				if err := st.ExitPaperSpotCarry(ctx, p.ID, "data_lost", paperExitConvergence(p, lastBasis)); err != nil {
					return exited, noFactWarn, fmt.Errorf("cex paper step: exit miss %d: %w", p.ID, err)
				}
				paperExitNote(ctx, st, p.ID, "data_lost", 0, lastFundingAnnOf(p), 0, miss)
				exited++
				continue
			}
			prev := 0.0
			if p.LastFundingAnn != nil {
				prev = *p.LastFundingAnn
			}
			if err := st.AccruePaperSpotCarry(ctx, p.ID, prev, 0, p.LowStreak, miss, lastBasis, nil, false); err != nil {
				return exited, noFactWarn, fmt.Errorf("cex paper step: miss %d: %w", p.ID, err)
			}
			continue
		}
		// S2 结息：perp 腿 funding 实付模拟（AccruePaperLegs 单腿——结算边界逐期实率，
		// 历史缺界回落当期率；sideFactor=+1 空 perp 收息）。raw symbol=canonical 同名
		// （CEX perp facts 键=venue+raw symbol；detector canonical 即 universe 规范化名）。
		legs := []PaperLegAccrue{
			{Kind: fact.KindFunding, Venue: keydomain.BareOf(c.PerpVenueKey), Symbol: p.Symbol, SideFactor: 1, CurRate: c.FundingAnn, CurOK: true},
		}
		recalc := p.LastAccrueTs == nil
		accruedAdd, settledTo, aok, aerr := AccruePaperLegs(ctx, st, legs, base, now, p.QtyUSD)
		if aerr != nil {
			return exited, noFactWarn, fmt.Errorf("cex paper step: accrue legs %d: %w", p.ID, aerr)
		}
		accrueTo := &settledTo
		if !aok {
			noFactWarn++
			settledTo = base
			accruedAdd = 0
			if recalc {
				accrueTo = nil // 「不全则留」：水位哨兵保留+accrued 旧值不动，下轮重试
			}
		}
		// low_streak：|funding| < 开仓下限同尺（normalized 判据=收益衰减）。
		streak := p.LowStreak
		if math.Abs(c.FundingAnn) < paperExitNormalizedPct {
			streak++
		} else {
			streak = 0
		}
		if p.Status != "active" {
			// demo 行只累积不自动退（同 spot/cfd 轨）。
			if err := st.AccruePaperSpotCarry(ctx, p.ID, c.FundingAnn, accruedAdd, p.LowStreak, 0, lastBasis, accrueTo, recalc); err != nil {
				return exited, noFactWarn, fmt.Errorf("cex paper step: accrue demo %d: %w", p.ID, err)
			}
			continue
		}
		// funding 翻负（方向收入转负）→ 当轮退 adverse（同 spot 轨 D-474a S3）。
		if c.FundingAnn < 0 {
			if err := st.ExitPaperSpotCarry(ctx, p.ID, "adverse", paperExitConvergence(p, lastBasis)); err != nil {
				return exited, noFactWarn, fmt.Errorf("cex paper step: exit adverse %d: %w", p.ID, err)
			}
			paperExitNote(ctx, st, p.ID, "adverse", c.FundingAnn, c.FundingAnn, 0, streak)
			exited++
			continue
		}
		// 满龄墙：paperMaxAgeDays（同 spot/cfd 轨）。
		if streak >= paperExitStreakRounds || now.Sub(p.EntryTs).Hours()/24 >= paperMaxAgeDays {
			reason := "normalized"
			if streak < paperExitStreakRounds {
				reason = "age_7d"
			}
			if err := st.ExitPaperSpotCarry(ctx, p.ID, reason, paperExitConvergence(p, lastBasis)); err != nil {
				return exited, noFactWarn, fmt.Errorf("cex paper step: exit %d: %w", p.ID, err)
			}
			paperExitNote(ctx, st, p.ID, reason, c.FundingAnn, c.FundingAnn, 0, streak)
			exited++
			continue
		}
		if err := st.AccruePaperSpotCarry(ctx, p.ID, c.FundingAnn, accruedAdd, streak, 0, lastBasis, accrueTo, recalc); err != nil {
			return exited, noFactWarn, fmt.Errorf("cex paper step: accrue %d: %w", p.ID, err)
		}
	}
	return exited, noFactWarn, nil
}

// candKeyOf 行→候选键（typed keys，与 StepPaperCexCarry activeByKey 同构）。
func candKeyOf(p pgstore.PaperSpotCarry) string {
	return candKeyOfLegs(p.VenueA, p.VenueTypeA, p.VenueB, p.VenueTypeB, p.Symbol)
}

func candKeyOfLegs(aV, aT, bV, bT, sym string) string {
	return aV + ":" + aT + "×" + bV + ":" + bT + "|" + sym
}
