// paper_spot_carry.go D-350 阶段②：spot-carry 纸面验证轨步进（开仓/累积/退出，零执行风险）。
// 记账口径：accrued += sideFactor×last_funding_ann/100/365×间隔天×qty（雷达快照费率，
// 诚实声明非逐结算值）；short_perp 收正 funding（sideFactor=+1），long_perp 收负 funding
// （sideFactor=−1，funding 为负 → 收入为正）。
package scan

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"time"

	"arb/internal/data/fact"
	"arb/internal/store/pgstore"
)

// 纸面轨参数（D-350 阶段②定值 + D-350R1 S4 闸校准 v1.1：band300+vol$10M+cap12+floor20——
// 校准依据=生产 fin_cex_universe 实查：net≥20 单闸放 221 行含死流动性长尾；band[20,300]+
// vol24h≥$10M=27 行，GUSDT/B2USDT/BRUSDT 全在）。
const (
	// PaperOpenNetPct 纸面开仓下限：net_spot_carry ≥ 20%/yr。
	PaperOpenNetPct = 20.0
	// PaperMaxNetPct 开仓上限（band 上限：≥300% 为死流动性长尾区，出 band 不开）。
	PaperMaxNetPct = 300.0
	// PaperMinVol24hUSD 流动性闸：24h 成交额下限（nil 亦不开——可交易性第一性判别）。
	PaperMinVol24hUSD = 1e7
	// PaperMaxActive 活跃纸面行硬上限（候选按 net 降序开到上限止）。
	PaperMaxActive = 12
	// PaperQtyUSD 虚拟仓位（固定 $1000，不复利）。
	PaperQtyUSD = 1000.0
	// paperExitNormalizedPct 退出线：|last_funding_ann| < 10%/yr。
	paperExitNormalizedPct = 10.0
	// paperExitStreakRounds 连续低于退出线的轮数。
	paperExitStreakRounds = 2
	// paperMaxAgeDays 满龄强评（天）。
	paperMaxAgeDays = 7.0
	// paperMissExitRounds D-474a S4：连续缺席轮次上限 → data_lost（缺席不计时不混 normalized）。
	paperMissExitRounds = 3
	// paperDemoIncomeNote D-474c S1：demo 行实收口径注记（幂等 marker；归属粒度上限
	// =venue+symbol，同所同币多仓并账=上限估值，flag_warn 语义落 note 透出）。
	paperDemoIncomeNote = "flag_warn: accrued=实收事件累计·归属=venue+symbol级(上限估值)"
)

// StepPaperSpotCarry 每轮扫描后调用：①开仓（20≤net≤300 ∧ vol24h≥$10M ∧ 持续≥48h ∧
// 连续 PaperEntryStreakRounds 轮全条件达标（D-383C 进场持续闸，entryStreak 由 wire_scan
// 装配一次逐轮传入）∧ active<12，达标候选按 net 降序开到上限止，streak 未满者不参与
// cap 竞争防尖峰占坑）②逐仓累积（active+demo，R1 S5）③退出（|funding|<10% 连续 2 轮 →
// normalized；满 7 天 → age_7d 强评——**仅 active**，demo 行只累积不自动退=人工/业主域）。
// 返回本轮开仓 symbol 清单（wire 侧聚一条开仓事件告警，D-350R1 S3）与退出行数。
// 起费用改读 venue 真值表（paper_fees.go），cexFeeBps 参数退役。
// flags：D-463 S2 KB→pipeline 投影旗标（wire 每轮开头 ListActiveFlags 一次传入；
// level=blocked 命中 (venue|symbol) → 跳过+log；warn 不拦——纸面=免费复核通道）。
func StepPaperSpotCarry(ctx context.Context, st PaperStore, entryStreak *EntryStreak, now time.Time, flags map[string]pgstore.VenueSymbolFlag) (opened []string, exited int, err error) {
	uni, err := st.ListCexUniverse(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("paper step: list cex universe: %w", err)
	}
	active, err := st.ListActivePaperSpotCarry(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("paper step: list active: %w", err)
	}
	// 累积清单在开仓前加载：本轮新开行次轮起算（开仓时 last_accrue_ts=DB now()，
	// 若后加载会立即吃掉一段「参数钟−DB 钟」偏移——R1 实测 2h 偏差根因）。
	accrable, err := st.ListAccrablePaperSpotCarry(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("paper step: list accrable: %w", err)
	}
	activeByKey := make(map[string]struct{}, len(active))
	for _, p := range active {
		activeByKey[p.VenuePair()+"|"+p.Symbol] = struct{}{}
	}
	// ①开仓（band+vol+cap v1.1；候选按 net 降序）
	type cand struct {
		key     string
		u       pgstore.CexUniverseRow
		venueA  string
		funding float64
		net     float64
	}
	cands := []cand{}
	for key, u := range uni {
		if u.Status != "TRADING" || (u.Tier != "T1" && u.Tier != "T3") || u.FundingAnn == nil {
			continue
		}
		if now.Sub(u.FirstSeen) < spotCarryMinAge {
			continue
		}
		net := abs(*u.FundingAnn) - SPOT_CARRY_COST_PCT
		if net < PaperOpenNetPct || net > PaperMaxNetPct {
			continue
		}
		if u.Vol24h == nil || *u.Vol24h < PaperMinVol24hUSD {
			continue
		}
		if _, ok := activeByKey[key]; ok {
			continue
		}
		// S2+返修R1：blocked 旗标硬拦——腿级展开查（venue 为组合串时单腿旗标同样命中）；
		// warn 级不拦（纸面=免费复核通道）。
		// fin_cex_universe.venue 生产零 × 裸域单列实证——spot 轨 venueA 直读，配对假设不成立。
		if f := blockedFlagHit(u.Venue, "", u.Symbol, nil, flags); f != nil {
			log.Printf("[paper spot] blocked flag 拦截: %s（%s@%s|%s）", key, f.Reason, f.Venue, f.Symbol)
			continue
		}
		cands = append(cands, cand{key: key, u: u, venueA: u.Venue, funding: *u.FundingAnn, net: net})
	}
	// 进场持续闸：连续 PaperEntryStreakRounds 轮全条件达标才放行——任一条件失败/
	// 候选从 uni 消失由 Step 清零（键=venue|symbol）；streak 未满的键不进开仓排序。
	passed := make([]string, 0, len(cands))
	for _, c := range cands {
		passed = append(passed, c.key)
	}
	eligible := entryStreak.Step(passed, PaperEntryStreakRounds)
	// 排序键=net 降序（|funding|−cost，与闸同键）。签名 funding 降序会把负 funding
	// （long_perp）恒排全部正侧之后——cap 命中时 long_perp 侧系统性饿死，且 age_7d 退出即重进
	// 使饿死可永久化（实证：ESPUSDT net 292.85 净序应榜首，签名序垫底进不了 cap）。
	sort.Slice(cands, func(i, j int) bool { return cands[i].net > cands[j].net })
	for _, c := range cands {
		if _, ok := eligible[c.key]; !ok {
			continue // streak 未满，不参与 cap 竞争（防尖峰占坑）
		}
		if len(activeByKey)+len(opened) >= PaperMaxActive {
			break
		}
		side := "short_perp"
		if c.funding < 0 {
			side = "long_perp"
		}
		r := &pgstore.PaperSpotCarry{
			VenueA: c.venueA, Symbol: c.u.Symbol, Side: side,
			EntryFundingAnn: c.funding, QtyUSD: PaperQtyUSD,
			EntryBasisBps: c.u.LastBasisBps, LastBasisBps: c.u.LastBasisBps, // D-354 S2：开仓基差快照（NULL 传播）
			EstRoundTripFeeUSD: paperSpotCarryFeeUSD(ctx, st, c.u.Venue, PaperQtyUSD), // D-474a S2：分腿真费四笔（venue 未覆盖→NULL 拒填）
		}
		if err := st.InsertPaperSpotCarry(ctx, r); err != nil {
			if err == pgstore.ErrPaperExists {
				continue
			}
			return opened, exited, fmt.Errorf("paper step: open %s: %w", c.key, err)
		}
		opened = append(opened, c.u.Symbol)
		entryStreak.Drop(c.key) // 开仓成功即删键（已 active，D-383C）
	}
	// ②累积（active+demo；D-474a S1 结算对齐逐期实率）+ ③退出（仅 active）
	noFactWarn := 0
	for _, p := range accrable {
		base := p.EntryTs
		if p.LastAccrueTs != nil {
			base = *p.LastAccrueTs
		}
		u, ok := uni[p.VenuePair()+"|"+p.Symbol]
		if p.Status == "demo" {
			// demo=真仓行——accrue 改吃 income_events 实收（不走 474a 结算
			// 对齐模型；纸面行照旧）。归属粒度上限=venue+symbol（同所同币多仓并账），
			// 行 note 幂等注 flag_warn 语义；缺席窗 +0 不外推；err 上抛禁吞。
			// income 归属=CEX 臂（venue_b 空=spot 单臂取 venue_a）
			incomeVenue := p.VenueB
			if incomeVenue == "" {
				incomeVenue = p.VenueA
			}
			sum, _, ierr := st.SumIncomeWindow(ctx, incomeVenue, p.Symbol, base, now)
			if ierr != nil {
				return opened, exited, fmt.Errorf("paper step: demo income %d: %w", p.ID, ierr)
			}
			demoLast := 0.0
			if p.LastFundingAnn != nil {
				demoLast = *p.LastFundingAnn
			}
			var demoBasis *float64
			if ok && u.FundingAnn != nil {
				demoLast = *u.FundingAnn
				demoBasis = u.LastBasisBps
			}
			// last_accrue_ts 对 demo 行语义=「实收已计入至 now」（缺席窗照样前推——
			// 实收口径下缺席=真 0 非缺数据，与纸面行「缺事实不前推」语义分家）。
			if err := st.AccruePaperSpotCarry(ctx, p.ID, demoLast, sum, p.LowStreak, 0, demoBasis, &now, false); err != nil {
				return opened, exited, fmt.Errorf("paper step: accrue demo %d: %w", p.ID, err)
			}
			if err := st.AnnotatePaperNote(ctx, p.ID, paperDemoIncomeNote); err != nil {
				return opened, exited, fmt.Errorf("paper step: demo note %d: %w", p.ID, err)
			}
			continue
		}
		if !ok || u.FundingAnn == nil {
			// 缺席轮不计时不 streak；连续缺席 ≥N 轮 → data_lost（仅 active）。
			miss := p.MissStreak + 1
			if miss >= paperMissExitRounds {
				if err := st.ExitPaperSpotCarry(ctx, p.ID, "data_lost", paperExitConvergence(p, p.LastBasisBps)); err != nil {
					return opened, exited, fmt.Errorf("paper step: exit miss %d: %w", p.ID, err)
				}
				paperExitNote(ctx, st, p.ID, "data_lost", 0, lastFundingAnnOf(p), 0, miss)
				exited++
				continue
			}
			prev := 0.0
			if p.LastFundingAnn != nil {
				prev = *p.LastFundingAnn
			}
			if err := st.AccruePaperSpotCarry(ctx, p.ID, prev, 0, p.LowStreak, miss, p.LastBasisBps, nil, false); err != nil {
				return opened, exited, fmt.Errorf("paper step: miss %d: %w", p.ID, err)
			}
			continue
		}
		f := *u.FundingAnn
		sideFactor := 1.0
		if p.Side == "long_perp" {
			sideFactor = -1.0
		}
		// S1：边界逐期实率累积（funding_next_settle+interval 反推界，funding_history 实率优先）。
		// recalc=存量行回迁哨兵（0139 置 NULL）：全生命周期重算+覆盖写；覆盖不全→留旧值下轮重试。
		recalc := p.LastAccrueTs == nil
		accruedAdd, settledTo, aok, aerr := AccruePaperLegs(ctx, st, []PaperLegAccrue{{
			Kind: fact.KindFunding, Venue: p.VenueA, Symbol: cexFactSymbol(p.Symbol), // D-550：p.Symbol=universe raw（gate BTC_USDT/okx -SWAP），facts 键=canonical 紧凑形
			SideFactor: sideFactor, CurRate: f, CurOK: true,
		}}, base, now, p.QtyUSD)
		if aerr != nil {
			return opened, exited, fmt.Errorf("paper step: accrue legs %d: %w", p.ID, aerr)
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
		if math.Abs(f) < paperExitNormalizedPct {
			streak++
		} else {
			streak = 0
		}
		lastBasis := u.LastBasisBps // D-354 S2：每轮随 uni 更新（map 既有加载，零新查询）
		// funding 翻号（方向收入转负）→ 当轮退 reason=adverse（不等 streak）。
		if sideFactor*f < 0 {
			if err := st.ExitPaperSpotCarry(ctx, p.ID, "adverse", paperExitConvergence(p, lastBasis)); err != nil {
				return opened, exited, fmt.Errorf("paper step: exit adverse %d: %w", p.ID, err)
			}
			paperExitNote(ctx, st, p.ID, "adverse", sideFactor*f, f, 0, streak)
			exited++
			continue
		}
		if streak >= paperExitStreakRounds || now.Sub(p.EntryTs).Hours()/24 >= paperMaxAgeDays {
			reason := "normalized"
			if streak < paperExitStreakRounds {
				reason = "age_7d"
			}
			// 退出冻结收敛捕获（本轮 last 缺→回退行内 last，双缺→NULL 诚实缺）。
			if err := st.ExitPaperSpotCarry(ctx, p.ID, reason, paperExitConvergence(p, lastBasis)); err != nil {
				return opened, exited, fmt.Errorf("paper step: exit %d: %w", p.ID, err)
			}
			paperExitNote(ctx, st, p.ID, reason, sideFactor*f, f, 0, streak)
			exited++
			continue
		}
		if err := st.AccruePaperSpotCarry(ctx, p.ID, f, accruedAdd, streak, 0, lastBasis, accrueTo, recalc); err != nil {
			return opened, exited, fmt.Errorf("paper step: accrue %d: %w", p.ID, err)
		}
	}
	if noFactWarn > 0 {
		log.Printf("[paper spot] 边界/率源缺 fail-closed 跳过 accrue: %d 行", noFactWarn)
	}
	return opened, exited, nil
}
