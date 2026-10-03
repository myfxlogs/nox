// paper_mtm_carry.go D-465：cfd×cfd（mt×mt）同品种跨商纸面轨。
// 收益来源=两商同品种 swap 利差（如长 A 付 −x%/yr + 空 B 收 +y%/yr）+跨商报价差。
// 纯纸面零执行：mt×mt 真钱需第二真户（业主域）+approve 执行面无 mt×mt 语义（不接）。
// 行形态复用 paper_spot_carry：VenueA×VenueB="mtA×mtB"，Structure="cfd_cfd"，
// MtVenue=short 腿 venue（D-465 钉死：mt_venue 列只放一腿，毕业行读此约定）。
package scan

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"arb/internal/store/pgstore"
)

// mt×mt 纸面轨参数（D-465 SSOT 定值）：
const (
	// PaperMtmOpenNetPct 开仓下限 net≥5%/yr——swap 利差系二阶收益，
	// 无成本数据支撑宁保守（SSOT 注释钉死取舍理由）。
	PaperMtmOpenNetPct = 5.0
	// PaperMtmMaxActive 独立小 cap=10（不占 spot/cfd 槽位额）。
	PaperMtmMaxActive = 10
	// paperMtmExitFloorPct 退出线：net < 1%/yr 连续 paperExitStreakRounds 轮 → normalized。
	paperMtmExitFloorPct = 1.0
	// paperMtmTickMaxAge entry_basis/费用 tick 新鲜闸（缺/陈旧→双 NULL 诚实缺，D-410 同语义）。
	paperMtmTickMaxAge = 5 * time.Minute
)

// mtmPair mt×mt 配对候选（纯计算产物）。venueA/venueB 对应 side 标签中的 A/B：
// long_a_short_b=A 长 B 空；short_a_long_b=A 空 B 长。
type mtmPair struct {
	venueA, venueB string
	side           string // "long_a_short_b" / "short_a_long_b"
	net            float64
	canonical      string
	longSym        string // 长腿 venue 的 broker 裸符号（tick 查询用）
	shortSym       string // 短腿 venue 的 broker 裸符号
	stale          bool
}

func (p mtmPair) key() string   { return p.venueA + "×" + p.venueB + "|" + p.canonical }
func (p mtmPair) venue() string { return p.venueA + "×" + p.venueB }

// bestMtmPair 纯函数：canonical 全部有序 venue 对 (A,B)（A≠B 自配硬排除）两结构净年化
// 取最大者——long_a_short_b：net=swap_long[A]+swap_short[B]；short_a_long_b 对偶。
// trade_mode 可执行性已在 collectMtSwaps 建簿时分方向过滤（long 簿=可买腿，short 簿=可空腿）。
// 确定性：net 并列时取 (venueA,venueB,side) 字典序小者。
func bestMtmPair(canonical string, longBook, shortBook map[string]mtSwap) (mtmPair, bool) {
	var best mtmPair
	found := false
	venues := map[string]struct{}{}
	for _, b := range []map[string]mtSwap{longBook, shortBook} {
		for _, s := range b {
			if s.canonical == canonical {
				venues[s.venue] = struct{}{}
			}
		}
	}
	try := func(p mtmPair) {
		if p.venueA == p.venueB {
			return
		}
		k := p.venueA + "|" + p.venueB + "|" + p.side
		bk := best.venueA + "|" + best.venueB + "|" + best.side
		if !found || p.net > best.net || (p.net == best.net && k < bk) {
			best, found = p, true
		}
	}
	for a := range venues {
		for b := range venues {
			if a == b {
				continue
			}
			la, laok := longBook[canonical+"|"+a]
			sa, saok := shortBook[canonical+"|"+a]
			lb, lbok := longBook[canonical+"|"+b]
			sb, sbok := shortBook[canonical+"|"+b]
			if laok && sbok {
				try(mtmPair{venueA: a, venueB: b, side: "long_a_short_b",
					net: la.val + sb.val, canonical: canonical,
					longSym: la.symbol, shortSym: sb.symbol, stale: la.stale || sb.stale})
			}
			if saok && lbok {
				try(mtmPair{venueA: a, venueB: b, side: "short_a_long_b",
					net: sa.val + lb.val, canonical: canonical,
					longSym: lb.symbol, shortSym: sa.symbol, stale: sa.stale || lb.stale})
			}
		}
	}
	return best, found
}

// latestMtmTick 最新 tick（≤5min）；缺/陈旧/非法价 → ok=false。
func latestMtmTick(ctx context.Context, st PaperStore, venue, symbol string, now time.Time) (bid, ask float64, ok bool) {
	ticks, err := st.TicksWindow(ctx, venue, symbol, now.Add(-paperMtmTickMaxAge))
	if err != nil || len(ticks) == 0 {
		return 0, 0, false
	}
	t := ticks[len(ticks)-1]
	if t.Bid <= 0 || t.Ask <= 0 || t.Ask < t.Bid {
		return 0, 0, false
	}
	return t.Bid, t.Ask, true
}

// mtmBasisFee 跨商价差+往返费估计（顺价方向：买长腿 ask、卖短腿 bid）。
// 任一腿 tick 缺/陈旧 → 双 NULL（D-410 拒填语义：可算不可毕业）。
func mtmBasisFee(ctx context.Context, st PaperStore, longVenue, longSym, shortVenue, shortSym string, now time.Time) (basisBps, feeUSD *float64) {
	lb, la, lok := latestMtmTick(ctx, st, longVenue, longSym, now)
	sb, sa, sok := latestMtmTick(ctx, st, shortVenue, shortSym, now)
	if !lok || !sok {
		return nil, nil
	}
	mid := (la + sb) / 2
	lm, sm := (la+lb)/2, (sa+sb)/2
	if mid <= 0 || lm <= 0 || sm <= 0 {
		return nil, nil
	}
	b := (la - sb) / mid * 1e4
	f := ((la-lb)/lm*1e4 + (sa-sb)/sm*1e4) / 2 * PaperQtyUSD / 1e4
	return &b, &f
}

// mtmTickLegs 行→(longVenue,longSym,shortVenue,shortSym)；簿缺腿 → ok=false。
func mtmTickLegs(p mtmPair) (lv, ls, sv, ss string) {
	if p.side == "long_a_short_b" {
		return p.venueA, p.longSym, p.venueB, p.shortSym
	}
	return p.venueB, p.longSym, p.venueA, p.shortSym
}

// StepPaperMtmCarry D-465：mt×mt 纸面轨步进——①开仓（net≥5 ∧ entryStreak 持续闸
// 键=venuePair|canonical ∧ 单键 active 去重 ∧ 独立 cap=10，候选按 net 降序）②累积
// （每轮重算当前 net_ann 套 Accrue 同族公式）③退出（net<1% 连续 2 轮→normalized；
// age≥7d→age_7d 强评；demo 行只累积不自动退）。wire_scan 在 cfd 步进后同轮串行调用。
// flags：D-463返修R2——blocked 旗标腿级展开硬拦开仓入口（venue=A×B 双腿均在展开集）；
// wire 每轮 ListActiveFlags 一次传入，nil=无旗放行。
func (sc *CarryUniverseScanner) StepPaperMtmCarry(ctx context.Context, st PaperStore,
	entryStreak *EntryStreak, now time.Time, flags map[string]pgstore.VenueSymbolFlag) (opened []string, exited int, err error) {
	longF, shortF, tmKnown, aliases, view, err := sc.loadMtSwapInputs(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("mtm paper step: %w", err)
	}
	var res ScanResult
	longBook, shortBook := sc.collectMtSwaps(ctx, longF, shortF, tmKnown, map[string]bool{}, &res, aliases, sc.swapFreeAccounts(ctx), view)
	active, err := st.ListActivePaperSpotCarry(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("mtm paper step: list active: %w", err)
	}
	accrable, err := st.ListAccrablePaperSpotCarry(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("mtm paper step: list accrable: %w", err)
	}
	activeByKey := map[string]struct{}{}
	mtmActive := 0
	for _, p := range active {
		if p.Structure != "cfd_cfd" {
			continue
		}
		mtmActive++
		activeByKey[p.VenuePair()+"|"+p.Symbol] = struct{}{}
	}
	// ①开仓：每 canonical 取最优配对（stale 腿不入配对）
	canonicals := map[string]struct{}{}
	for _, s := range longBook {
		canonicals[s.canonical] = struct{}{}
	}
	pairs := []mtmPair{}
	for c := range canonicals {
		p, ok := bestMtmPair(c, longBook, shortBook)
		if !ok || p.stale || p.net < PaperMtmOpenNetPct {
			continue
		}
		if _, dup := activeByKey[p.venue()+"|"+c]; dup {
			continue
		}
		if f := blockedFlagHit(p.venueA, p.venueB, c, nil, flags); f != nil {
			log.Printf("[mtm paper] blocked flag 拦截: %s（%s@%s|%s）", p.key(), f.Reason, f.Venue, f.Symbol)
			continue
		}
		pairs = append(pairs, p)
	}
	passed := make([]string, 0, len(pairs))
	for _, p := range pairs {
		passed = append(passed, p.key())
	}
	eligible := entryStreak.Step(passed, PaperEntryStreakRounds)
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].net > pairs[j].net })
	for _, p := range pairs {
		if _, ok := eligible[p.key()]; !ok {
			continue
		}
		if mtmActive+len(opened) >= PaperMtmMaxActive {
			break
		}
		lv, ls, sv, ss := mtmTickLegs(p)
		basis, fee := mtmBasisFee(ctx, st, lv, ls, sv, ss, now)
		shortLegVenue := sv
		r := &pgstore.PaperSpotCarry{
			VenueA: p.venueA, VenueB: p.venueB, Symbol: p.canonical,
			Side: p.side, EntryFundingAnn: p.net, QtyUSD: PaperQtyUSD,
			EntryBasisBps: basis, LastBasisBps: basis,
			EstRoundTripFeeUSD: fee,
			Structure:          "cfd_cfd", MtVenue: &shortLegVenue,
		}
		if err := st.InsertPaperSpotCarry(ctx, r); err != nil {
			if err == pgstore.ErrPaperExists {
				continue
			}
			return opened, exited, fmt.Errorf("mtm paper step: open %s: %w", p.key(), err)
		}
		opened = append(opened, r.VenuePair()+":"+r.Symbol)
		entryStreak.Drop(p.key())
	}
	// D-500 S1：venue 静默探测——accrable cfd_cfd 行任 MT 腿 venue 在本轮 swap 簿零条目
	// → 休市/断供非机会消失；供 accrue 段 suspend 冻结 miss_streak（pos502 同病灶）。
	seenMtVenue := make(map[string]bool)
	for _, s := range longBook {
		seenMtVenue[s.venue] = true
	}
	for _, s := range shortBook {
		seenMtVenue[s.venue] = true
	}
	silentMt := make(map[string]bool)
	for _, p := range accrable {
		if p.Structure != "cfd_cfd" {
			continue
		}
		for _, v := range []string{p.VenueA, p.VenueB} {
			if v != "" && !seenMtVenue[v] {
				silentMt[v] = true
			}
		}
	}
	// ②累积 + ③退出（仅 cfd_cfd 行）——D-474a 起拆至 paper_mtm_accrue.go。
	n, err := accrueExitMtmPaper(ctx, st, accrable, longBook, shortBook, silentMt, now)
	if err != nil {
		return opened, exited, err
	}
	exited += n
	return opened, exited, nil
}

// legsVenue 行配对双列取长/短腿 venue（long=true→长腿；D-498 直读 VenueA/VenueB）。
func legsVenue(p pgstore.PaperSpotCarry, long bool) string {
	if p.Side == "long_a_short_b" {
		if long {
			return p.VenueA
		}
		return p.VenueB
	}
	if long {
		return p.VenueB
	}
	return p.VenueA
}
