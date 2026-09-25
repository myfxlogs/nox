// paper_perp.go D-376 B 面：perp×perp 纸面验证轨（divergence 对开仓/逐轮重算/退出）。
// 触发：9-21 业主令「真腿可以进纸面」——divergence 本身是 delta-neutral funding 差收割
// 信号（short 高 funding 所、long 低 funding 所），纸面=oracle 口径证据层，真腿对同样可验证
// 且不需要 sim 授权。复用 paper_spot_carry 表+store 方法（structure='perp_perp'，mt_venue=NULL；
// side='perp_perp' 表无 CHECK，D-376 自审实证）。radar 是 cron 进程（1h 轮），不碰 core 热路径。
package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/scan"
	"arb/internal/store/pgstore"
)

// 参数常量（依据钉死）：
const (
	// paperPerpSuspectAnn 病态尾部闸：任一腿 |funding|>300%/yr 或 net>300 → suspect
	// （G/PROVE 实证：极端负 funding 的 clamp 价≠实盘，纸面会记假账；覆盖 real/sim 两域）。
	paperPerpSuspectAnn = 300.0
	// paperPerpMaxActive perp×perp 活跃行硬上限（独立于 cfd 6/spot 12，各轨不互挤）。
	paperPerpMaxActive = 8
	// perpMissExitRounds D-474a S4：连续缺席轮次 → data_lost（与 scan 轨 paperMissExitRounds=3 同值口径）。
	perpMissExitRounds = 3
	// perpExitStreakRounds 连续低净/缺席轮数（与 internal/scan paperExitStreakRounds 同语义；
	// 该常量 scan 包未导出 → 本地定义。radar 1h 轮 ×2 ≈ 2h，比 cfd 轨 8h 敏感——miss=数据死宁可早退）。
	perpExitStreakRounds = 2
	// perpMaxAgeDays 满龄强评（天；与 scan paperMaxAgeDays 同语义，未导出故本地定义）。
	perpMaxAgeDays = 7.0
	// perpTickMaxAge D-474b S1：两腿 mark 价差 tick 新鲜闸（同 mtm 轨 paperMtmTickMaxAge=5min 口径）。
	perpTickMaxAge = 5 * time.Minute
)

// isSuspectPair 病态尾部判定：任一腿 |funding|>paperPerpSuspectAnn 或 net>paperPerpSuspectAnn。
// net 恒 ≥divPct>0（short=高侧构造），无绝对值必要；suspect 对不开仓不入 streak，只进 digest。
func isSuspectPair(p divPair) bool {
	return absAnn(p.FundShort) > paperPerpSuspectAnn || absAnn(p.FundLong) > paperPerpSuspectAnn ||
		p.NetAnn > paperPerpSuspectAnn
}

func absAnn(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// venueIsRealOrUnknown D-376 告警分层谓词：simMap（ListVenueIsReal，venueKey→*bool）查
// venue+":perp"——真(true)/未知(缺行或 nil) → 真腿处理（digest 化+纸面）；确证 sim(false) → 既有
// event 路径。simMap 空（查询失败）→ 全部当真腿（保守，不冒充 sim）。
func venueIsRealOrUnknown(simMap map[string]*bool, venue string) bool {
	b, ok := simMap[venue+":perp"]
	return !ok || b == nil || *b
}

// splitDivPairs D-376 S3 告警分层（纯函数，可单测）：suspect → suspectList（digest 计数）；
// 任一侧真/未知 → realPairs（digest 行+纸面，不走告警 event）；双腿确证 sim → simPairs
// （既有 event+cooldown+alert 路径零变更，同时入纸面）。
func splitDivPairs(pairs []divPair, simMap map[string]*bool) (simPairs, realPairs []divPair, suspectList []string) {
	for _, p := range pairs {
		if isSuspectPair(p) {
			suspectList = append(suspectList, p.Canon)
			continue
		}
		if venueIsRealOrUnknown(simMap, p.ShortVenue) || venueIsRealOrUnknown(simMap, p.LongVenue) {
			realPairs = append(realPairs, p)
			continue
		}
		simPairs = append(simPairs, p)
	}
	sort.Strings(suspectList) // map/多源迭代 → digest 输出确定
	return simPairs, realPairs, suspectList
}

// buildFundIndex D-376 S2：radar 内存行双索引——①venue|rawSymbol→funding（重算取数）；
// ②venue|canonical→rawSymbol（纸面行 symbol 存 canonical，反查原始行）。
// 歧义处置：同 venue 下同 canonical 多 raw 行（如 binance 1000SHIB/SHIB 类）取 FundingAnn
// 非 nil 首行——nil 行作占位、后续非 nil 覆写（不猜取哪行报价，只认有费率行）。
func buildFundIndex(rows []normRow) (fundByKey map[string]*float64, symByVenueCanon map[string]string) {
	fundByKey = make(map[string]*float64, len(rows))
	symByVenueCanon = make(map[string]string, len(rows))
	for _, n := range rows {
		fundByKey[n.Venue+"|"+n.Symbol] = n.FundingAnn
		k := n.Venue + "|" + n.Canonical
		if cur, ok := symByVenueCanon[k]; !ok {
			symByVenueCanon[k] = n.Symbol
		} else if fundByKey[n.Venue+"|"+cur] == nil && n.FundingAnn != nil {
			symByVenueCanon[k] = n.Symbol
		}
	}
	return fundByKey, symByVenueCanon
}

// stepPaperPerpCarry D-376 B 面：perp×perp 纸面轨步进（radar 每轮、pairs=非 suspect 全量——
// 冷却只挡告警不挡纸面；行在=信号在）。
// ①开仓：venue=ShortVenue×LongVenue（方向在键）、symbol=Canon、side/structure='perp_perp'、
//
//	entry_funding_ann=NetAnn、qty=PaperQtyUSD；net 降序防 cap 饿死（同 D-374 教训）。
//
// ②每轮重算：signedNet = fund[short|rawSym]−fund[long|rawSym]（方向入场固定，重算可转负——
//
//	转负则 accrued 衰减，这正是要测的「信号会不会反噬」）；accrual = signedNet/100/365×days×qty
//	（无 sideFactor——net 已含方向，同 cfd 轨口径）。
//
// ③退出（仅 active）：任一侧行缺席/nil → miss streak++；signedNet<divPct → streak++
//
//	（divPct 传参复用不开第二魔数）；连续 perpExitStreakRounds 轮 → normalized；满 perpMaxAgeDays
//	→ age_7d。demo 行只累积不自动退；缺席时 demo 跳过不误记。basis 列 NULL 诚实缺（radar 行无 basis）。
func stepPaperPerpCarry(ctx context.Context, st *pgstore.Store, pairs []divPair, rows []normRow, divPct float64, now time.Time) (opened []string, exited int, err error) {
	if st == nil {
		return nil, 0, nil // 未接线（测试）→ 诚实零步进
	}
	active, err := st.ListActivePaperSpotCarry(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("perp paper step: list active: %w", err)
	}
	accrable, err := st.ListAccrablePaperSpotCarry(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("perp paper step: list accrable: %w", err)
	}
	activeByKey := make(map[string]struct{}, len(active))
	perpActive := 0
	for _, p := range active {
		activeByKey[p.Venue+"|"+p.Symbol] = struct{}{}
		if p.Structure == "perp_perp" {
			perpActive++
		}
	}
	// ① 开仓（suspect 双保险：splitDivPairs 已滤，此处再闸防调用方直传）
	cands := []divPair{}
	for _, p := range pairs {
		if isSuspectPair(p) {
			continue
		}
		key := p.ShortVenue + "×" + p.LongVenue + "|" + p.Canon
		if _, ok := activeByKey[key]; ok {
			continue
		}
		cands = append(cands, p)
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].NetAnn > cands[j].NetAnn })
	for _, p := range cands {
		if perpActive+len(opened) >= paperPerpMaxActive {
			break
		}
		r := &pgstore.PaperSpotCarry{
			Venue: p.ShortVenue + "×" + p.LongVenue, Symbol: p.Canon, Side: "perp_perp",
			EntryFundingAnn: p.NetAnn, QtyUSD: scan.PaperQtyUSD, Structure: "perp_perp",
			EstRoundTripFeeUSD: scan.PaperPerpPerpFeeUSD(p.ShortVenue, p.LongVenue, scan.PaperQtyUSD), // D-474a S2
		}
		// D-474b S1：开仓 basis=两腿 mark 价差（tick 缺/陈旧 → NULL 缺账诚实缺，不降级常数）。
		if b := perpTickBasis(ctx, st, p.ShortVenue, p.ShortSym, p.LongVenue, p.LongSym, now); b != nil {
			r.EntryBasisBps, r.LastBasisBps = b, b
		}
		if err := st.InsertPaperSpotCarry(ctx, r); err != nil {
			if err == pgstore.ErrPaperExists {
				continue
			}
			return opened, exited, fmt.Errorf("perp paper step: open %s: %w", r.Venue, err)
		}
		opened = append(opened, p.Canon)
	}
	// ②③ 重算+退出（仅 perp_perp 行；spot/cfd 行各轨自理不触碰）
	// D-474a S1：双腿各自结算边界逐期实率（共同前缀=min 水位）；S4：缺席不计时，
	// 连缺 perpMissExitRounds 轮 → data_lost；S3：signedNet<0 → 当轮退 adverse。
	fundByKey, symByVenueCanon := buildFundIndex(rows)
	noFactWarn := 0
	for _, p := range accrable {
		if p.Structure != "perp_perp" {
			continue
		}
		parts := strings.SplitN(p.Venue, "×", 2)
		if len(parts) != 2 {
			continue // 非法组合键防御（不猜方向）
		}
		base := p.EntryTs
		if p.LastAccrueTs != nil {
			base = *p.LastAccrueTs
		}
		shortVenue, longVenue := parts[0], parts[1]
		shortSym := symByVenueCanon[shortVenue+"|"+p.Symbol]
		longSym := symByVenueCanon[longVenue+"|"+p.Symbol]
		fs, okS := fundByKey[shortVenue+"|"+shortSym]
		fl, okL := fundByKey[longVenue+"|"+longSym]
		if !okS || !okL || fs == nil || fl == nil || shortSym == "" || longSym == "" {
			if p.Status != "active" {
				continue // demo 缺席：无数据不误记
			}
			// S4：缺席不计入 normalized streak；连缺 → data_lost
			miss := p.MissStreak + 1
			if miss >= perpMissExitRounds {
				if err := st.ExitPaperSpotCarry(ctx, p.ID, "data_lost", scan.PaperExitConvergence(p, nil)); err != nil {
					return opened, exited, fmt.Errorf("perp paper step: exit miss %d: %w", p.ID, err)
				}
				exited++
				continue
			}
			prev := 0.0
			if p.LastFundingAnn != nil {
				prev = *p.LastFundingAnn
			}
			if err := st.AccruePaperSpotCarry(ctx, p.ID, prev, 0, p.LowStreak, miss, nil, nil, false); err != nil {
				return opened, exited, fmt.Errorf("perp paper step: miss %d: %w", p.ID, err)
			}
			continue
		}
		signedNet := *fs - *fl          // 方向入场固定，重算可转负（反噬即衰减，诚实记账）
		recalc := p.LastAccrueTs == nil // D-474a S1 存量回迁哨兵
		// D-474b S1：每轮现算两腿 mark 价差 → last_basis（缺 tick→nil，accrue 写 NULL 诚实缺；
		// 退出收敛=entry 对本轮 last 冻结，同 cfd 轨 paperExitConvergence 口径）。
		lastBasis := perpTickBasis(ctx, st, shortVenue, shortSym, longVenue, longSym, now)
		accruedAdd, settledTo, aok, aerr := scan.AccruePaperLegs(ctx, st, []scan.PaperLegAccrue{
			{Kind: fact.KindFunding, Venue: shortVenue, Symbol: shortSym, SideFactor: +1, CurRate: *fs, CurOK: true},
			{Kind: fact.KindFunding, Venue: longVenue, Symbol: longSym, SideFactor: -1, CurRate: *fl, CurOK: true},
		}, base, now, p.QtyUSD)
		if aerr != nil {
			return opened, exited, fmt.Errorf("perp paper step: accrue legs %d: %w", p.ID, aerr)
		}
		accrueTo := &settledTo
		if !aok {
			noFactWarn++
			settledTo = base
			accruedAdd = 0
			if recalc {
				accrueTo = nil // 「不全则留」：水位哨兵保留，下轮重试
			}
		}
		streak := p.LowStreak
		if signedNet < divPct {
			streak++
		} else {
			streak = 0
		}
		if p.Status != "active" {
			// demo 行只累积不自动退（同 spot/cfd 轨）
			if err := st.AccruePaperSpotCarry(ctx, p.ID, signedNet, accruedAdd, p.LowStreak, 0, lastBasis, accrueTo, recalc); err != nil {
				return opened, exited, fmt.Errorf("perp paper step: accrue demo %d: %w", p.ID, err)
			}
			continue
		}
		// D-474a S3：signedNet 翻负 → 当轮退 adverse（不等 streak）。
		if signedNet < 0 {
			if err := st.ExitPaperSpotCarry(ctx, p.ID, "adverse", scan.PaperExitConvergence(p, lastBasis)); err != nil {
				return opened, exited, fmt.Errorf("perp paper step: exit adverse %d: %w", p.ID, err)
			}
			exited++
			continue
		}
		if streak >= perpExitStreakRounds {
			if err := st.ExitPaperSpotCarry(ctx, p.ID, "normalized", scan.PaperExitConvergence(p, lastBasis)); err != nil {
				return opened, exited, fmt.Errorf("perp paper step: exit %d: %w", p.ID, err)
			}
			exited++
			continue
		}
		if now.Sub(p.EntryTs).Hours()/24 >= perpMaxAgeDays {
			if err := st.ExitPaperSpotCarry(ctx, p.ID, "age_7d", scan.PaperExitConvergence(p, lastBasis)); err != nil {
				return opened, exited, fmt.Errorf("perp paper step: exit age %d: %w", p.ID, err)
			}
			exited++
			continue
		}
		if err := st.AccruePaperSpotCarry(ctx, p.ID, signedNet, accruedAdd, streak, 0, lastBasis, accrueTo, recalc); err != nil {
			return opened, exited, fmt.Errorf("perp paper step: accrue %d: %w", p.ID, err)
		}
	}
	if noFactWarn > 0 {
		fmt.Printf("[perp paper] 边界/率源缺 fail-closed 跳过 accrue: %d 行\n", noFactWarn)
	}
	return opened, exited, nil
}
