// paper_cex_carry.go —— D-555 S1：双CEX（FUNDING_HEDGE spot买×perp卖）纸面轨步进——保底槽+发现槽。
// 业主令（方向锚）：所有组合必须走统一漏斗（纸面→毕业→授权）；候选源=detector.FundingHedgeDetector.Scan
// 纯函数枚举（cmd/core 转换直传，P3 单源禁重写枚举逻辑）。行 shape：
// structure='long_spot_short_perp'（正构型 v1 策略钉，engine regime 闸同一钉）、
// venue_a/b=裸 venue、venue_type_a/b=spot/perp（D-496 拆列惯例，venue_key 生成列投影键域）、
// mt_venue=NULL、side='short_perp'（perp 腿做空收正 funding）。
// S1 范围=开仓生产；计费/basis/结息/退出=S2（行 est_fee/basis NULL 诚实缺，不臆造）。
package scan

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/store/pgstore"
)

// PaperCexOpts 装配参数（cmd/core 注入；D-050 数量槽退役——AuthorizedMaxActive/
// MaxActive 删，只留质量闸：data_lost 冷却+双腿报价连续性+D-051 深度/funding）。
type PaperCexOpts struct {
	ReopenCooldown time.Duration // 保底 flap 冷却（退出行 max(exit_ts) 窗）
	// D-051② 质量闸阈（>0 生效；缺省源=NOX_PAPER_CEX_MIN_DEPTH_USD/MIN_FUNDING_PCT）。
	MinDepthUSD   float64           // 双腿 fin_cex_depth 24h 均 mid_depth_usd 下限；无样本 fail-closed
	MinFundingPct float64           // perp 腿 funding_history 7d 年化均值下限（%）；无样本 fail-closed
	Skips         *PaperSkipCounter // D-051③：入口拦截原因计数（nil=不记，selfwatch 分布源）
}

// CexPaperCand 双CEX 候选（cmd/core 由 detector.Candidate Type==FUNDING_HEDGE 转换直传）。
type CexPaperCand struct {
	Canonical    string
	SpotVenueKey string  // "{venue}:{venue_type}"（detector 原样透传）
	PerpVenueKey string  // 同上
	FundingAnn   float64 // perp 腿 funding 年化（百分点口径，与 universe FundingAnn 同源同单位）
	SpotAsk      float64 // D-555 S2：spot 腿卖价（detector PriceBuy 原样——basis/est 基础）
	PerpBid      float64 // D-555 S2：perp 腿买价（detector PriceSell 原样）
}

// StepPaperCexCarry 每轮引擎扫描后调用（FUNDING_HEDGE 候选直传；零候选零 DB 开销）：
// ①保底段：enabled∧非 simulated 白名单组合（spot×perp 同 canonical 构型）不套 net 下限
//
//	自动开纸面（候选缺席=数据缺席不开——同 mt×cex「universe 未产行」语义）；
//
// ②发现段：D-051 候选源=detector Allow 前流（白名单/净阈不前置）——准入收口为本步
//
//	质量闸：双腿 24h 均深度≥阈 ∧ perp 7d funding 均值≥阈 ∧ D-042 双闸
//	（data_lost 冷却+双腿近 1h 报价）后全量开——D-050 数量槽退役。
//
// blocked 旗标腿级拦截（同 D-463 口径）；flap 冷却按退出行 max(exit_ts)（保底段同 mt×cex）；
// 跳过原因计数→opts.Skips 累计（selfwatch 日报分布，D-051③）+ skipN 逐调返回（轮聚合日志源）。
// R1①：退出/计息与开仓解耦——本函数只做开仓闸（hook 候选到点即调）；
// accrueExitCexPaper 归 StepPaperCexCarryAccrueExit 每轮一次（部署实证：hook 子集候选
// 驱动整段 pass → 新开行 miss_streak 秒满 data_lost，TRUMPUSDT #66978/#66979 同秒开退）。
func StepPaperCexCarryOpen(ctx context.Context, st PaperStore, cands []CexPaperCand, now time.Time, opts PaperCexOpts, flags map[string]pgstore.VenueSymbolFlag, quoteAlive func(venueKey, symbol string) (time.Time, bool)) (opened []string, skipN map[string]int, err error) {
	active, err := st.ListActivePaperSpotCarry(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("cex paper step: list active: %w", err)
	}
	// 活跃行键（键域口径：venue_key_a×venue_key_b|symbol——type 列参与键，与 mt×cex
	// 裸域键天然不撞）。D-050：diversity 槽记账随数量槽退役，只留在仓去重。
	cexRowKey := func(p pgstore.PaperSpotCarry) string {
		return keydomain.Join(p.VenueA, p.VenueTypeA) + "×" + keydomain.Join(p.VenueB, p.VenueTypeB) + "|" + p.Symbol
	}
	activeByKey := make(map[string]struct{}, len(active))
	for _, p := range active {
		if p.Structure != "long_spot_short_perp" {
			continue
		}
		activeByKey[cexRowKey(p)] = struct{}{}
	}
	candByKey := make(map[string]CexPaperCand, len(cands))
	for _, c := range cands {
		candByKey[c.SpotVenueKey+"×"+c.PerpVenueKey+"|"+c.Canonical] = c
	}
	// D-051②：质量闸读面每轮缓存——funding_history 全表均值一轮一读；深度按
	// venue×canonical 键去重（同所双腿天然同行=一次查询两判）。
	type depthStat struct {
		avg float64
		n   int
		err error
	}
	depthCache := map[string]depthStat{}
	depthOf := func(venue, canonical string) (float64, int, error) {
		k := venue + "|" + canonical
		if r, ok := depthCache[k]; ok {
			return r.avg, r.n, r.err
		}
		avg, n, err := st.CexDepthAvgUSD(ctx, venue, canonical, now.Add(-24*time.Hour))
		depthCache[k] = depthStat{avg, n, err}
		return avg, n, err
	}
	var fundingAvgs map[string]map[string]pgstore.SettleAvg
	var fundingErr error
	fundingLoaded := false
	fundingAvgOf := func(venue, canonical string) (pgstore.SettleAvg, bool) {
		if !fundingLoaded {
			fundingAvgs, fundingErr = st.FundingSettleAvgs(ctx, now.Add(-7*24*time.Hour))
			fundingLoaded = true
		}
		if fundingErr != nil {
			return pgstore.SettleAvg{}, false
		}
		sa, ok := fundingAvgs[venue][canonical]
		return sa, ok
	}
	// openCand 返回 ""=已开，否则跳过原因标签（D-051②：depth_missing/depth_low/
	// funding_low/quote_dead/cooldown 五闸因 + flag/exists/insert）——计数+日报分布源。
	openCand := func(c CexPaperCand, key string) string {
		if f := blockedFlagHit(keydomain.BareOf(c.SpotVenueKey), keydomain.BareOf(c.PerpVenueKey), c.Canonical, nil, flags); f != nil {
			log.Printf("[cex paper] blocked flag 拦截: %s（%s@%s|%s）", key, f.Reason, f.Venue, f.Symbol)
			return "flag"
		}
		// D-051②a：双腿 24h 均 mid_depth_usd ≥ MinDepthUSD（fail-closed——读失败/
		// 任一无样本同拒；同所双腿命中同 venue×symbol 行=同一深度证据）。
		if opts.MinDepthUSD > 0 {
			spAvg, spN, spErr := depthOf(spVenue(c), c.Canonical)
			ppAvg, ppN, ppErr := depthOf(ppVenue(c), c.Canonical)
			if spErr != nil || ppErr != nil {
				log.Printf("[cex paper] 深度读失败 拦开: %s（spot=%v perp=%v）", key, spErr, ppErr)
				return "depth_missing"
			}
			if spN == 0 || ppN == 0 {
				return "depth_missing" // 无样本不开（D-058 fail-closed；零覆盖所天然排除）
			}
			if spAvg < opts.MinDepthUSD || ppAvg < opts.MinDepthUSD {
				return "depth_low"
			}
		}
		// D-051②b：perp 腿 funding_history 7d 年化均值 ≥ MinFundingPct（fail-closed——
		// 读失败/无样本同拒；funding 缺席=证据价值零，计 funding_low 档）。
		if opts.MinFundingPct > 0 {
			sa, ok := fundingAvgOf(ppVenue(c), c.Canonical)
			if fundingErr != nil {
				log.Printf("[cex paper] funding 均值读失败 拦开: %s（%v）", key, fundingErr)
				return "funding_low"
			}
			if !ok || sa.Avg < opts.MinFundingPct {
				return "funding_low"
			}
		}
		// ②：双腿近 1h 报价缺席=0 才开——双腿各自 bus 最新报价时间 1h 内
		//（LastChangeAt 价格变更钟；funding facts 不作报价活性源——R1 退回②）。
		// R2 ①：补时间窗——!ok（从未变更）或 last change 距今 >1h 均拦（陈旧报价非活性）。
		// quoteAlive nil=未接线（fail-open 跳过闸；生产 wire 必注入 qbus.LastChangeAt）。
		if quoteAlive != nil {
			if t, ok := quoteAlive(c.SpotVenueKey, c.Canonical); !ok || now.Sub(t) > time.Hour {
				log.Printf("[cex paper] 报价连续性不足 拦开: %s（spot 腿近 1h 无报价变更）", key)
				return "quote_dead"
			}
			if t, ok := quoteAlive(c.PerpVenueKey, c.Canonical); !ok || now.Sub(t) > time.Hour {
				log.Printf("[cex paper] 报价连续性不足 拦开: %s（perp 腿近 1h 无报价变更）", key)
				return "quote_dead"
			}
		}
		// ①：data_lost 冷却守卫——同对 24h 内 ≥2 次 data_lost 退出 → 24h 内不再开仓
		//（openCand 入口统一生效：保底段+发现段两路同闸）。
		if dataLostCooldown(ctx, st, spVenue(c), ppVenue(c), c.Canonical, now) {
			log.Printf("[cex paper] data_lost 冷却 拦开: %s（24h 内 ≥2 次 data_lost）", key)
			return "cooldown"
		}
		sp, pp := keydomain.Parse(c.SpotVenueKey), keydomain.Parse(c.PerpVenueKey)
		p := &pgstore.PaperSpotCarry{
			VenueA: sp.Venue, VenueTypeA: sp.Type,
			VenueB: pp.Venue, VenueTypeB: pp.Type,
			Symbol: c.Canonical, Side: "short_perp",
			EntryFundingAnn: c.FundingAnn, QtyUSD: PaperQtyUSDAt(ctx, st),
			Structure: "long_spot_short_perp",
		}
		// est_fee=两腿 CEX 真费（spot 往返+perp 往返，paper_fees 真值表；
		// venue 未覆盖→NULL 诚实缺拒填——同 spot/cfd 轨口径）；basis=perp−spot 腿间
		// 溢价 bps（qbus 双腿报价直取，禁 monitor 簿——FUNDING_HEDGE 本就豁免 D-335 闸）。
		p.EstRoundTripFeeUSD = PaperSpotPerpFeeUSD(ctx, st, sp.Venue, pp.Venue, PaperQtyUSDAt(ctx, st))
		if c.SpotAsk > 0 && c.PerpBid > 0 {
			bps := (c.PerpBid - c.SpotAsk) / c.SpotAsk * 1e4
			p.EntryBasisBps, p.LastBasisBps = &bps, &bps
		}
		if err := st.InsertPaperSpotCarry(ctx, p); err != nil {
			if err == pgstore.ErrPaperExists {
				return "exists"
			}
			// 单行失败不炸整轮（paper 轨惯例），WP1 禁静默：日志留痕续跑。
			log.Printf("[cex paper] insert %s: %v", key, err)
			return "insert"
		}
		activeByKey[key] = struct{}{}
		opened = append(opened, c.Canonical)
		return ""
	}
	// ① 保底段：enabled∧非 simulated 组合先行（不占发现槽额）。
	combos, err := st.LoadXAssetAllowCombos(ctx, "real")
	if err != nil {
		return opened, nil, fmt.Errorf("cex paper step: load allow combos: %w", err)
	}
	authorizedKeys := make(map[string]struct{}, len(combos))
	for _, c := range combos {
		sp, pp, canon, spType, ppType, ok := cexSpotPerpComboLegs(c)
		if !ok {
			continue // 非 spot×perp 同 canonical 构型（mt×cex 归 cfd 轨；余者不进本轨）
		}
		key := keydomain.Join(sp, spType) + "×" + keydomain.Join(pp, ppType) + "|" + canon
		authorizedKeys[key] = struct{}{}
	}
	// ②/D-051③：跳过原因计数（opts.Skips 累计→日报分布；skipN 逐调返回→轮聚合日志）。
	skipN = map[string]int{}
	count := func(reason string) {
		if reason == "" {
			return
		}
		skipN[reason]++
		opts.Skips.Add(reason)
	}
	for _, c := range combos {
		sp, pp, canon, spType, ppType, ok := cexSpotPerpComboLegs(c)
		if !ok {
			continue
		}
		key := keydomain.Join(sp, spType) + "×" + keydomain.Join(pp, ppType) + "|" + canon
		if _, isActive := activeByKey[key]; isActive {
			continue
		}
		// flap 冷却：退出行 max(exit_ts) 窗内不重开（同 D-410 钉死②——防衰减组合反复记费）。
		if opts.ReopenCooldown > 0 {
			lastExit, err := st.LatestPaperExitTs(ctx, sp, pp, canon)
			if err != nil {
				return opened, skipN, fmt.Errorf("cex paper step: exit ts %s: %w", key, err)
			}
			if lastExit != nil && now.Sub(*lastExit) < opts.ReopenCooldown {
				count("cooldown")
				continue // D-042 R1：拦截本体在 openCand 双闸；可见性归 selfwatch data_lost 项（R1 ③）
			}
		}
		cand, ok := candByKey[key]
		if !ok {
			continue // 候选缺席=本轮无 funding/报价面（数据缺席不开，同 universe 未产行）
		}
		count(openCand(cand, key))
	}
	// ② 发现段：其余候选经质量闸+D-042 双闸全量开（D-050 无槽）。
	for _, c := range cands {
		key := c.SpotVenueKey + "×" + c.PerpVenueKey + "|" + c.Canonical
		if _, isActive := activeByKey[key]; isActive {
			continue
		}
		if _, authorized := authorizedKeys[key]; authorized {
			continue // 保底段已裁决（冷却跳过态不重复计入发现段）
		}
		count(openCand(c, key))
	}
	return opened, skipN, nil
}

// StepPaperCexCarryAccrueExit R1①：计息+退出轮 pass——每轮一次（装配侧 30s
// 轮驱动，票定「独立 30s 轮」二选一写明：miss_streak 3 轮≈90s 保原缺席语义，
// CFD 4h 对齐轮会把 data_lost 拖到 12h+）。cands=本轮 unfiltered 全量快照
// （R1② 同源：与开仓同一 ScanUnfiltered 流；零候选=缺席照常跑退出行）。
// 返回退出数+率源缺跳过数（accrueSkip 归轮聚合日志键）。
func StepPaperCexCarryAccrueExit(ctx context.Context, st PaperStore, cands []CexPaperCand, now time.Time) (exited, accrueSkip int, err error) {
	accrable, err := st.ListAccrablePaperSpotCarry(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("cex paper step: list accrable: %w", err)
	}
	candByKey := make(map[string]CexPaperCand, len(cands))
	for _, c := range cands {
		candByKey[c.SpotVenueKey+"×"+c.PerpVenueKey+"|"+c.Canonical] = c
	}
	return accrueExitCexPaper(ctx, st, accrable, candByKey, now)
}

// spVenue/ppVenue CexPaperCand 双腿裸 venue。
func spVenue(c CexPaperCand) string { return keydomain.Parse(c.SpotVenueKey).Venue }
func ppVenue(c CexPaperCand) string { return keydomain.Parse(c.PerpVenueKey).Venue }

// dataLostCooldown D-042 R1 ①：data_lost 冷却守卫——同 symbol×venue_a×venue_b 对 24h 内
// ≥2 次 data_lost 退出 → true（24h 内不再 paper 开仓；查 paper_spot_carry 自身不加表）。
// R2 ②：计数查询失败=true fail-closed 拦（读口故障不误放——冷却判定不可用即不开仓）。
func dataLostCooldown(ctx context.Context, st PaperStore, venueA, venueB, canonical string, now time.Time) bool {
	n, err := st.CountDataLostExits(ctx, venueA, venueB, canonical, now.Add(-24*time.Hour))
	if err != nil {
		log.Printf("[cex paper] data_lost 计数失败 %s×%s|%s: %v（fail-closed 拦）", venueA, venueB, canonical, err)
		return true
	}
	return n >= 2
}
