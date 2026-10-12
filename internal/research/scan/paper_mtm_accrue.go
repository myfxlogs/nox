// paper_mtm_accrue.go D-474a：mt×mt 纸面轨累积+退出段（自 paper_mtm_carry.go 拆出，
// 300 行规模门禁）。S1 双腿 swap 日界结算对齐（facts 键=broker 裸符号），
// S3 net 翻负当轮退 adverse，S4 腿缺/陈旧=缺席轮独立 miss_streak → data_lost。
package scan

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/data/fact"
	"arb/internal/store/pgstore"
)

// accrueExitMtmPaper cfd_cfd 行累积与退出（返回本轮退出数）。
// longBook/shortBook=本轮 MT swap 簿（key=canonical|venue，含 raw symbol 与 stale 标）。
func accrueExitMtmPaper(ctx context.Context, st PaperStore, accrable []pgstore.PaperSpotCarry,
	longBook, shortBook map[string]mtSwap, silentMt map[string]bool, now time.Time) (int, error) {
	exited := 0
	noFactWarn := 0
	for _, p := range accrable {
		if p.Structure != "cfd_cfd" {
			continue
		}
		if p.VenueA == "" || p.VenueB == "" {
			continue // 非双腿配对行防御（不猜方向）
		}
		base := p.EntryTs
		if p.LastAccrueTs != nil {
			base = *p.LastAccrueTs
		}
		// 重算当前 net：long_a_short_b=swap_long[A]+swap_short[B]；short_a_long_b 对偶。
		var lLeg, sLeg mtSwap
		if p.Side == "long_a_short_b" {
			lLeg, sLeg = longBook[p.Symbol+"|"+p.VenueA], shortBook[p.Symbol+"|"+p.VenueB]
		} else {
			sLeg, lLeg = shortBook[p.Symbol+"|"+p.VenueA], longBook[p.Symbol+"|"+p.VenueB]
		}
		lok := lLeg.symbol != "" && sLeg.symbol != "" && !lLeg.stale && !sLeg.stale
		net := 0.0
		var basis *float64
		if lok {
			net = lLeg.val + sLeg.val
			basis, _ = mtmBasisFee(ctx, st, legsVenue(p, true), lLeg.symbol, legsVenue(p, false), sLeg.symbol, now)
		}
		// 腿缺/陈旧=缺席轮——不计时不 streak；连续缺席 ≥N 轮 → data_lost。
		if !lok {
			if p.Status != "active" {
				continue // demo 行缺席：不误记
			}
			// suspend：任 MT 腿 venue 整轮零产出（休市/断供）→ session 关闭非
			// 机会消失——不计 miss_streak、不写 accrue；仅留 age_7d 墙钟出口防僵尸。
			if silentMt[p.VenueA] || silentMt[p.VenueB] {
				if now.Sub(p.EntryTs).Hours()/24 >= paperMaxAgeDays {
					if err := st.ExitPaperSpotCarry(ctx, p.ID, "age_7d", paperExitConvergence(p, p.LastBasisBps)); err != nil {
						return exited, fmt.Errorf("mtm paper step: exit age suspend %d: %w", p.ID, err)
					}
					paperExitNote(ctx, st, p.ID, "age_7d", 0, 0, 0, p.MissStreak)
					exited++
				}
				continue
			}
			miss := p.MissStreak + 1
			if miss >= paperMissExitRounds {
				if err := st.ExitPaperSpotCarry(ctx, p.ID, "data_lost", paperExitConvergence(p, p.LastBasisBps)); err != nil {
					return exited, fmt.Errorf("mtm paper step: exit miss %d: %w", p.ID, err)
				}
				paperExitNote(ctx, st, p.ID, "data_lost", 0, 0, 0, miss)
				exited++
				continue
			}
			prev := 0.0
			if p.LastFundingAnn != nil {
				prev = *p.LastFundingAnn
			}
			if err := st.AccruePaperSpotCarry(ctx, p.ID, prev, 0, p.LowStreak, miss, p.LastBasisBps, nil, false); err != nil {
				return exited, fmt.Errorf("mtm paper step: miss %d: %w", p.ID, err)
			}
			continue
		}
		// S1：双 MT 腿日界逐期 swap 实率（facts 键=broker 裸符号=lLeg/sLeg.symbol）。
		longV, shortV := legsVenue(p, true), legsVenue(p, false)
		recalc := p.LastAccrueTs == nil // D-474a S1 存量回迁哨兵（0139 置 NULL）
		accruedAdd, settledTo, aok, aerr := AccruePaperLegs(ctx, st, []PaperLegAccrue{
			{Kind: fact.KindSwapLong, Venue: longV, Symbol: lLeg.symbol, SideFactor: 1},
			{Kind: fact.KindSwapShort, Venue: shortV, Symbol: sLeg.symbol, SideFactor: 1},
		}, base, now, p.QtyUSD)
		if aerr != nil {
			return exited, fmt.Errorf("mtm paper step: accrue legs %d: %w", p.ID, aerr)
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
		streak := p.LowStreak
		if net < paperMtmExitFloorPct {
			streak++
		} else {
			streak = 0
		}
		if p.Status != "active" {
			// demo 行只累积不自动退（出场=人工/业主域，同 spot/cfd 轨惯例）
			if err := st.AccruePaperSpotCarry(ctx, p.ID, net, accruedAdd, p.LowStreak, 0, basis, accrueTo, recalc); err != nil {
				return exited, fmt.Errorf("mtm paper step: accrue demo %d: %w", p.ID, err)
			}
			continue
		}
		// net 翻负 → 当轮退 reason=adverse。
		if net < 0 {
			if err := st.ExitPaperSpotCarry(ctx, p.ID, "adverse", paperExitConvergence(p, basis)); err != nil {
				return exited, fmt.Errorf("mtm paper step: exit adverse %d: %w", p.ID, err)
			}
			paperExitNote(ctx, st, p.ID, "adverse", net, lLeg.val, sLeg.val, streak)
			exited++
			continue
		}
		if streak >= paperExitStreakRounds || now.Sub(p.EntryTs).Hours()/24 >= paperMaxAgeDays {
			reason := "normalized"
			if streak < paperExitStreakRounds {
				reason = "age_7d"
			}
			if err := st.ExitPaperSpotCarry(ctx, p.ID, reason, paperExitConvergence(p, basis)); err != nil {
				return exited, fmt.Errorf("mtm paper step: exit %d: %w", p.ID, err)
			}
			paperExitNote(ctx, st, p.ID, reason, net, lLeg.val, sLeg.val, streak)
			exited++
			continue
		}
		if err := st.AccruePaperSpotCarry(ctx, p.ID, net, accruedAdd, streak, 0, basis, accrueTo, recalc); err != nil {
			return exited, fmt.Errorf("mtm paper step: accrue %d: %w", p.ID, err)
		}
	}
	if noFactWarn > 0 {
		log.Printf("[mtm paper] 率源缺 fail-closed 跳过 accrue: %d 行", noFactWarn)
	}
	return exited, nil
}
