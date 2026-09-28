// paper_cfd_accrue.go D-474a：cfd 纸面轨累积+退出段（自 paper_cfd_carry.go 拆出，
// 300 行规模门禁）。语义同原 StepPaperCfdCarry ②③ 段：
// S1 双腿结算对齐累积（cex funding 腿 raw symbol 键 + MT swap 腿 broker 裸符号键），
// S3 net 翻负当轮退 adverse，S4 缺席独立 miss_streak → data_lost。
package scan

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// accrueExitCfdPaper cfd 行累积与退出（返回本轮退出数）。rowByKey=本轮 universe
// clean cfd 行索引（key=mtV×cexV|canonical）；accrable=active+demo 行全集。
func accrueExitCfdPaper(ctx context.Context, st PaperStore, accrable []pgstore.PaperSpotCarry,
	rowByKey map[string]UniverseRow, dirtyByKey map[string]string, silentMt map[string]bool, now time.Time) (int, error) {
	exited := 0
	// D-474a S2：cex 腿 canonical→raw symbol 索引（funding 边界事实键=venue+raw symbol；
	// HedgeCanonical/Symbol 双路归一键——取 universe 行 Symbol 为 raw）。
	cexRawByCanon := map[string]string{}
	if uniRows, uerr := st.ListCexUniverse(ctx); uerr == nil {
		for _, ur := range uniRows {
			for _, canon := range []string{ur.HedgeCanonical, ur.Symbol} {
				if k := ur.Venue + "|" + NormalizeJoinKey(canon); canon != "" {
					cexRawByCanon[k] = ur.Symbol
				}
			}
		}
	}
	// S1 MT 腿 canonical→broker 裸符号（facts 键=raw；venue 维度懒加载一次）。
	mtRawCache := map[string]map[string]string{}
	mtRaw := func(venue, canonical string) string {
		m, ok := mtRawCache[venue]
		if !ok {
			m = map[string]string{}
			if maps, err := st.ListSymbolMappings(ctx, venue); err == nil {
				for _, mm := range maps {
					if mm.Canonical != "" {
						m[NormalizeJoinKey(mm.Canonical)] = mm.BrokerSymbol
					}
				}
			}
			mtRawCache[venue] = m
		}
		return m[NormalizeJoinKey(canonical)]
	}
	noFactWarn := 0
	for _, p := range accrable {
		if !cfdStructures[p.Structure] {
			continue
		}
		base := p.EntryTs
		if p.LastAccrueTs != nil {
			base = *p.LastAccrueTs
		}
		// D-410 S4：last_basis 每轮更 monitor 最新行（陈旧闸行龄超限→nil 诚实缺写 NULL）。
		var lastBasis *float64
		pair := paperCfdPairKey(p.Symbol)
		// D-423：三元组 venue 键——D-498 直读配对双列（venue_a=MT 臂、venue_b=CEX 臂）。
		mtV, cexV := p.VenueA, p.VenueB
		cexVenue, mtVenue := monitorVenueKeys(cexV, mtV)
		entryBps, _, _, _, ok, err := st.LatestXAssetEntryGate(ctx, pair, cexVenue, mtVenue, paperCfdMonitorMaxAge)
		if err != nil {
			return exited, fmt.Errorf("cfd paper step: monitor gate %s: %w", pair, err)
		}
		if ok {
			lastBasis = &entryBps
		}
		key := p.VenuePair() + "|" + p.Symbol
		r, ok := rowByKey[key]
		if !ok {
			if p.Status != "active" {
				continue // demo 行缺席：无数据不误记
			}
			// D-500 S1 suspend：venue 整轮零产出（休市/断供）或行被质量剔除 → session 关闭
			// 非机会消失——不计 miss_streak、不写 accrue；仅留 age_7d 墙钟出口防僵尸。
			if _, dirty := dirtyByKey[key]; silentMt[mtV] || dirty {
				if now.Sub(p.EntryTs).Hours()/24 >= paperMaxAgeDays {
					if err := st.ExitPaperSpotCarry(ctx, p.ID, "age_7d", paperExitConvergence(p, lastBasis)); err != nil {
						return exited, fmt.Errorf("cfd paper step: exit age suspend %d: %w", p.ID, err)
					}
					exited++
				}
				continue
			}
			// D-474a S4：缺席不计时不 streak；连续缺席 ≥N 轮 → data_lost（不混 normalized）。
			miss := p.MissStreak + 1
			if miss >= paperMissExitRounds {
				if err := st.ExitPaperSpotCarry(ctx, p.ID, "data_lost", paperExitConvergence(p, lastBasis)); err != nil {
					return exited, fmt.Errorf("cfd paper step: exit miss %d: %w", p.ID, err)
				}
				exited++
				continue
			}
			prev := 0.0
			if p.LastFundingAnn != nil {
				prev = *p.LastFundingAnn
			}
			if err := st.AccruePaperSpotCarry(ctx, p.ID, prev, 0, p.LowStreak, miss, lastBasis, nil, false); err != nil {
				return exited, fmt.Errorf("cfd paper step: miss %d: %w", p.ID, err)
			}
			continue
		}
		// S1 双腿结算对齐：cex funding 腿（raw symbol 键）+ MT swap 腿（broker 裸符号键）。
		// Side="short_perp"→结构 long_cfd_short_perp：perp 腿空（收 funding，+1）/MT 腿长（swap_long）。
		swapKind, sideFactor := fact.KindSwapLong, 1.0
		if p.Side == "long_perp" {
			swapKind, sideFactor = fact.KindSwapShort, -1.0
		}
		rawCex := cexRawByCanon[cexV+"|"+NormalizeJoinKey(p.Symbol)]
		rawMt := mtRaw(mtV, p.Symbol)
		legs := []PaperLegAccrue{
			{Kind: fact.KindFunding, Venue: cexV, Symbol: rawCex, SideFactor: sideFactor, CurRate: r.FundingAnn, CurOK: rawCex != ""},
			{Kind: swapKind, Venue: mtV, Symbol: rawMt, SideFactor: 1},
		}
		recalc := p.LastAccrueTs == nil // D-474a S1 存量回迁哨兵（0139 置 NULL）
		accruedAdd, settledTo, aok, aerr := float64(0), base, false, error(nil)
		if rawCex != "" && rawMt != "" { // canonical→raw 映射缺 → 边界事实键无着落，fail-closed 不查
			accruedAdd, settledTo, aok, aerr = AccruePaperLegs(ctx, st, legs, base, now, p.QtyUSD)
		}
		if aerr != nil {
			return exited, fmt.Errorf("cfd paper step: accrue legs %d: %w", p.ID, aerr)
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
		if r.NetAnnual < PaperCfdOpenNetPct {
			streak++
		} else {
			streak = 0
		}
		if p.Status != "active" {
			// demo 行只累积不自动退（同 spot 轨）
			if err := st.AccruePaperSpotCarry(ctx, p.ID, r.FundingAnn, accruedAdd, p.LowStreak, 0, lastBasis, accrueTo, recalc); err != nil {
				return exited, fmt.Errorf("cfd paper step: accrue demo %d: %w", p.ID, err)
			}
			continue
		}
		// D-474a S3：net carry 翻负（funding 减成本转负）→ 当轮退 reason=adverse。
		if r.NetAnnual < 0 {
			if err := st.ExitPaperSpotCarry(ctx, p.ID, "adverse", paperExitConvergence(p, lastBasis)); err != nil {
				return exited, fmt.Errorf("cfd paper step: exit adverse %d: %w", p.ID, err)
			}
			exited++
			continue
		}
		// D-548 S2：零收益僵尸槽——三判据全中当轮退（accrued 仍 $0 + 入场超 24h +
		// 本轮同源行 net<开仓下限）。判据 net 取同轮 rowByKey 行（防跨窗比较）；
		// note 记三判据值留档（AnnotatePaperNote 幂等追加）；已在带零收益行不杀。
		if p.Status == "active" && p.AccruedUSD+accruedAdd == 0 &&
			now.Sub(p.EntryTs) >= paperZeroAccrualStaleHours*time.Hour && r.NetAnnual < PaperCfdOpenNetPct {
			if err := st.ExitPaperSpotCarry(ctx, p.ID, "zero_accrual_stale", paperExitConvergence(p, lastBasis)); err != nil {
				return exited, fmt.Errorf("cfd paper step: exit zero_accrual %d: %w", p.ID, err)
			}
			marker := fmt.Sprintf("zero_accrual_stale 判据: accrued=$%.2f age=%.1fh net=%.2f<thr=%.1f",
				p.AccruedUSD+accruedAdd, now.Sub(p.EntryTs).Hours(), r.NetAnnual, PaperCfdOpenNetPct)
			if err := st.AnnotatePaperNote(ctx, p.ID, marker); err != nil {
				log.Printf("[cfd paper] zero_accrual note %d: %v", p.ID, err)
			}
			exited++
			continue
		}
		if streak >= paperExitStreakRounds || now.Sub(p.EntryTs).Hours()/24 >= paperMaxAgeDays {
			reason := "normalized"
			if streak < paperExitStreakRounds {
				reason = "age_7d"
			}
			if err := st.ExitPaperSpotCarry(ctx, p.ID, reason, paperExitConvergence(p, lastBasis)); err != nil {
				return exited, fmt.Errorf("cfd paper step: exit %d: %w", p.ID, err)
			}
			exited++
			continue
		}
		if err := st.AccruePaperSpotCarry(ctx, p.ID, r.FundingAnn, accruedAdd, streak, 0, lastBasis, accrueTo, recalc); err != nil {
			return exited, fmt.Errorf("cfd paper step: accrue %d: %w", p.ID, err)
		}
	}
	if noFactWarn > 0 {
		log.Printf("[cfd paper] 边界/率源缺 fail-closed 跳过 accrue: %d 行", noFactWarn)
	}
	return exited, nil
}
