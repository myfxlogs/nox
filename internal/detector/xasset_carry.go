package detector

import (
	"context"
	"math"
	"time"

	"arb/internal/bus"
)

// AssetEquivEntry 标的等价类行（detector 本地视图；pgstore.AssetEquivEntry
// 经装配层适配进来——detector 不 import store 层）。
// Venue=QuoteBus 运行时键：CEX=所名（"binance"），MT 类=账号 ID（"3001157"，提示词裁 8）。
type AssetEquivEntry struct {
	AssetClass string
	Canonical  string
	VenueType  string
	Venue      string
}

// AssetEquivSource 等价类映射读取口（D-300 裁 5：单实例注入，禁 Scan 内查库）。
// pgstore.AssetEquivProvider 在装配层包一层适配实现。
type AssetEquivSource interface {
	ByClass() map[string][]AssetEquivEntry
}

// XAssetCarryDetector 检测同 asset_class 跨 canonical 的套利机会（D-300 §5）。
// 与 CrossVenueDetector 的区别：配对不同 canonical（PAXG↔XAU），而非同 canonical 跨 venue。
// 结构：卖 perp 腿收 funding，买 MT 腿付 swap；净息差（年化 pp）= funding + swap_long(buy MT)
// + swap_short(sell MT)——swap fact 为负=付息自然扣减（swap-free venue 由 adapter 源头
// 置 0：mt_swap.go swapFreeServerPrefixes，bc388c43；detector 原样消费）。
type XAssetCarryDetector struct {
	EntryMinBps     float64          // 进场价差门槛（一次性 bps；可负=容忍小幅逆价进场）
	MinNetAnnualPct float64          // 净息差门槛（funding−swap 年化百分点）
	TrackingMaxBps  float64          // 跟踪误差上限（一次性 bps，§5.3 默认 50）
	FreshnessTTL    time.Duration    // 报价新鲜度
	AssetEquiv      AssetEquivSource // 标的等价类映射（单实例注入）
}

func (d *XAssetCarryDetector) Type() OppType { return TypeXAssetCarry }

// Scan 对每个 asset_class 做跨 canonical 配对扫描。
// 配对集合 = asset_equiv enabled 行按 canonical 分组后两两组合（同一 canonical 可有多 venue 行）。
// 双腿 venue 组合各试双向（buy A sell B / buy B sell A），funding 只挂 perp 卖腿。
func (d *XAssetCarryDetector) Scan(_ context.Context, snapshot map[string]map[string]bus.Quote, fp FactProvider) []Candidate {
	if d.AssetEquiv == nil {
		return nil
	}
	now := time.Now()
	var funding map[string]map[string]float64
	var swap map[string]map[string][2]float64
	var modes map[string]map[string]int64
	var realMap map[string]*bool
	if fp != nil {
		funding = fp.FundingFacts()
		swap = fp.SwapFacts()
		modes = fp.TradeModes()
		realMap = fp.IsRealMap()
	}
	var out []Candidate
	for class, entries := range d.AssetEquiv.ByClass() {
		byCanon := make(map[string][]AssetEquivEntry)
		for _, e := range entries {
			byCanon[e.Canonical] = append(byCanon[e.Canonical], e)
		}
		if len(byCanon) < 2 {
			continue
		}
		canons := make([]string, 0, len(byCanon))
		for c := range byCanon {
			canons = append(canons, c)
		}
		sortStrings(canons)
		for i := range canons {
			for j := i + 1; j < len(canons); j++ {
				ca, cb := canons[i], canons[j]
				for _, ea := range byCanon[ca] {
					qa, ok := snapshot[ca][ea.Venue+":"+ea.VenueType]
					if !ok {
						continue
					}
					for _, eb := range byCanon[cb] {
						qb, ok := snapshot[cb][eb.Venue+":"+eb.VenueType]
						if !ok {
							continue
						}
						// 正向：买 ca 卖 cb；反向：买 cb 卖 ca
						if c := d.tryPair(class, ca, ea, qa, cb, eb, qb, funding, swap, modes, realMap, now); c != nil {
							out = append(out, *c)
						}
						if c := d.tryPair(class, cb, eb, qb, ca, ea, qa, funding, swap, modes, realMap, now); c != nil {
							out = append(out, *c)
						}
					}
				}
			}
		}
	}
	return out
}

// tryPair 评估一个方向：buy（按 ask 买 bc）/ sell（按 bid 卖 sc）。
// 门槛序：新鲜度 → legTradable（裁 7 沿用）→ isRealPair（D-319：D-311 S7 起 IsRealMap=
// MT 账户+CEX 合并视图，「XAU 腿是 MT 非 CEX real pair」的 v1 跳过理由失效，虚实同层配对）
// → 跟踪误差 → 进场价差 → 净息差。
func (d *XAssetCarryDetector) tryPair(
	class, bc string, be AssetEquivEntry, bq bus.Quote,
	sc string, se AssetEquivEntry, sq bus.Quote,
	funding map[string]map[string]float64, swap map[string]map[string][2]float64,
	modes map[string]map[string]int64, realMap map[string]*bool, now time.Time,
) *Candidate {
	if stale(bq, now, d.FreshnessTTL) || stale(sq, now, d.FreshnessTTL) {
		return nil
	}
	if !legTradable(modes, bc, bq.VenueKey(), true) || !legTradable(modes, sc, sq.VenueKey(), false) {
		return nil
	}
	if !isRealPair(realMap, bq.VenueKey(), sq.VenueKey()) {
		return nil
	}
	// 跟踪误差（§5.3）：分母=卖腿 mid（canonical_b 对应 sc）。
	bm, sm := bq.Mid(), sq.Mid()
	if bm <= 0 || sm <= 0 {
		return nil
	}
	if math.Abs(bm-sm)/sm*10000 > d.TrackingMaxBps {
		return nil
	}
	// 进场价差（一次性 bps）：与 evaluator S5 同口径 mid=(PriceBuy+PriceSell)/2。
	mid := (bq.Ask + sq.Bid) / 2
	if mid <= 0 {
		return nil
	}
	if (sq.Bid-bq.Ask)/mid*10000 < d.EntryMinBps {
		return nil
	}
	// 净息差年化 pp：funding（perp 卖腿收/买腿付，缺 fact 不评估）+ MT 腿 swap（bps_daily→%/年=×365/100）。
	var net float64
	if se.VenueType == "perp" {
		f, ok := lookupFunding(funding, sc, se.Venue)
		if !ok {
			return nil
		}
		net += f // 空腿收 funding（负 funding 自然变负项）
	}
	if be.VenueType == "perp" {
		f, ok := lookupFunding(funding, bc, be.Venue)
		if !ok {
			return nil
		}
		net -= f // 多腿付 funding
	}
	net += swapAnnualPct(swap, bc, be, true)
	net += swapAnnualPct(swap, sc, se, false)
	if net < d.MinNetAnnualPct {
		return nil
	}
	qt := bq.Time
	if sq.Time.After(qt) {
		qt = sq.Time
	}
	return &Candidate{
		Type:          TypeXAssetCarry,
		Canonical:     bc,
		CanonicalSell: sc,
		AssetClass:    class,
		VenueBuy:      bq.VenueKey(),
		VenueSell:     sq.VenueKey(),
		PriceBuy:      bq.Ask,
		PriceSell:     sq.Bid,
		GrossBps:      net,
		QuoteTime:     qt.UnixMilli(),
	}
}

// swapAnnualPct MT 腿 swap 年化百分点：long=true 取 swap_long[0]，false 取 swap_short[1]；
// bps_daily × 365 / 100 → 年化 %（负值=付息自然为净息差负项）。非 MT 腿/无 fact → 0。
func swapAnnualPct(swap map[string]map[string][2]float64, canonical string, e AssetEquivEntry, long bool) float64 {
	if e.VenueType != "mt4" && e.VenueType != "mt5" {
		return 0
	}
	pair, ok := lookupSwap(swap, canonical, e.Venue)
	if !ok {
		return 0
	}
	if long {
		return pair[0] * 365 / 100
	}
	return pair[1] * 365 / 100
}
