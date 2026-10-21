package detector

import (
	"context"
	"math"
	"nox/internal/data/keydomain"
	"time"

	"nox/internal/data/bus"
	"nox/internal/platform/rejects"
)

// rejects 注册：xasset_trypair 面门集（D-652 拒因可观测——注册了不挂/挂了没注册由对拍锚防）。
func init() {
	rejects.Register("xasset_trypair", "allow", "stale_buy", "stale_sell", "leg_tradable", "is_real", "mid_nonpos", "tracking", "entry_min", "funding_absent", "net_min", "tenor")
}

// AssetEquivEntry 标的等价类行（detector 本地视图；pgstore.AssetEquivEntry
// 经装配层适配进来——detector 不 import store 层）。
// Venue=QuoteBus 运行时键：CEX=所名（"binance"），MT 类=账号 ID（"3001157"，提示词裁 8）。
type AssetEquivEntry struct {
	AssetClass string
	Canonical  string
	VenueType  string
	Venue      string
	// TenorDays 交割腿剩余天数（vt=futures 必填>0——basis 年化分母；0=未知
	// fail-closed 不产候选）。非交割腿留零。
	TenorDays float64
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
	EntryMinBps     float64           // 进场价差门槛（一次性 bps；可负=容忍小幅逆价进场）
	MinNetAnnualPct float64           // 净息差门槛（funding−swap 年化百分点）
	TrackingMaxBps  float64           // 跟踪误差上限（一次性 bps，§5.3 默认 50）
	FreshnessTTL    time.Duration     // 报价新鲜度
	AssetEquiv      AssetEquivSource  // 标的等价类映射（单实例注入）
	CfdHints        CfdPairHintSource // D-403 S4：universe-hint cfd×perp 配对（nil=跳过向后兼容）
	// Allow D-017 发现域收口：nil=不过滤（既有测试装配）；非 nil=白名单前置滤——
	// (canonical, venue 对) 不在 xasset_allow_combos.enabled 内则静默不发射
	// （零候选零日志，评估省略；执行闸=autotrade 即时 specs 仍为权威）。
	Allow func(canonical, canonicalSell, venueBuy, venueSell string) bool
	// DeliveryHints D-649续③：armed 交割配对提示源（perp_delivery 机会断供修——
	// 毕业批放行≠发现层扫；按 xasset_allow_combos 含 futures 腿行枚举）。
	DeliveryHints DeliveryHintSource
}

// CfdPairHint D-403 S4：universe-hint cfd×perp 配对提示——asset_equiv 之外的授权驱动
// 配对空间（同 canonical cfd×perp=BTC 型，asset_equiv 零 crypto+Scan 严格跨 canonical 的
// 配对盲区解法）。canonical/venueKey 已由装配层经 scan.ResolveGraduationCanonicals 单源解析。
type CfdPairHint struct {
	MtCanonical  string
	CexCanonical string
	MtVenueKey   string // "{mt_venue}:{mt4|mt5}"
	CexVenueKey  string // "{cex}:perp"
}

// CfdPairHintSource 配对提示读取口（照 AssetEquivSource 模式：单实例注入禁 Scan 内查库；
// nil=跳过，向后兼容既有构造点）。
type CfdPairHintSource interface {
	CfdPairs(ctx context.Context) []CfdPairHint
}

func (d *XAssetCarryDetector) Type() OppType { return TypeXAssetCarry }

// Scan 对每个 asset_class 做跨 canonical 配对扫描。
// 配对集合 = asset_equiv enabled 行按 canonical 分组后两两组合（同一 canonical 可有多 venue 行）。
// 双腿 venue 组合各试双向（buy A sell B / buy B sell A），funding 只挂 perp 卖腿。
func (d *XAssetCarryDetector) Scan(ctx context.Context, snapshot map[string]map[string]bus.Quote, fp FactProvider) []Candidate {
	// AssetEquiv 与 CfdHints/DeliveryHints 各自独立可 nil（等价类路与 hint
	// 路径解耦；hint 源可在 asset_equiv 零 crypto 的盲区独立产候选）。
	if d.AssetEquiv == nil && d.CfdHints == nil && d.DeliveryHints == nil {
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
	if d.AssetEquiv != nil {
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
						qa, ok := snapshot[ca][keydomain.Join(ea.Venue, ea.VenueType)]
						if !ok {
							continue
						}
						for _, eb := range byCanon[cb] {
							qb, ok := snapshot[cb][keydomain.Join(eb.Venue, eb.VenueType)]
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
	}
	// universe-hint 配对路径——复用 tryPair 全门禁（新鲜度/tradable/isReal/
	// tracking/价差/净息差），零新增评估口径；同 canonical（Mt==Cex）天然支持；
	// AssetClass 留空（Candidate 语义：同标的机会留空）。
	if d.CfdHints != nil {
		for _, h := range d.CfdHints.CfdPairs(ctx) {
			mq, ok := snapshot[h.MtCanonical][h.MtVenueKey]
			if !ok {
				continue
			}
			cq, ok := snapshot[h.CexCanonical][h.CexVenueKey]
			if !ok {
				continue
			}
			me := AssetEquivEntry{Venue: bareVenue(h.MtVenueKey), VenueType: mtVenueTypeOf(h.MtVenueKey)}
			ce := AssetEquivEntry{Venue: bareVenue(h.CexVenueKey), VenueType: "perp"}
			// 正向：买 MT 卖 CEX；反向：买 CEX 卖 MT（双向全门禁，funding 挂 perp 腿语义随 tryPair）
			if c := d.tryPair("", h.MtCanonical, me, mq, h.CexCanonical, ce, cq, funding, swap, modes, realMap, now); c != nil {
				out = append(out, *c)
			}
			if c := d.tryPair("", h.CexCanonical, ce, cq, h.MtCanonical, me, mq, funding, swap, modes, realMap, now); c != nil {
				out = append(out, *c)
			}
		}
	}
	out = append(out, d.scanDeliveryHints(ctx, snapshot, funding, swap, modes, realMap, now)...)
	return out
}

// bareVenue venue_key 前段 → 裸 venue（funding/swap fact 表键=裸 venue，tryPair 经 entry.Venue 查）。
func bareVenue(venueKey string) string {
	return keydomain.BareOf(venueKey)
}

// mtVenueTypeOf venue_key 后缀 → MT venueType（tryPair 的 swapAnnualPct 只认 mt4/mt5）。
func mtVenueTypeOf(venueKey string) string {
	if keydomain.IsType(venueKey, "mt4") {
		return "mt4"
	}
	return "mt5"
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
	// 发现域收口：白名单外组合在评估前静默剔除（不产候选不刷日志；
	// 等价类路与 hint 路两径同经此单点）。
	if d.Allow != nil && !d.Allow(bc, sc, bq.VenueKey(), sq.VenueKey()) {
		rejects.Inc(rejects.Key{Face: "xasset_trypair", Gate: "allow"})
		return nil
	}
	if stale(bq, now, d.FreshnessTTL) {
		rejects.Inc(rejects.Key{Face: "xasset_trypair", Gate: "stale_buy"})
		return nil
	}
	if stale(sq, now, d.FreshnessTTL) {
		rejects.Inc(rejects.Key{Face: "xasset_trypair", Gate: "stale_sell"})
		return nil
	}
	if !legTradable(modes, bc, bq.VenueKey(), true) || !legTradable(modes, sc, sq.VenueKey(), false) {
		rejects.Inc(rejects.Key{Face: "xasset_trypair", Gate: "leg_tradable"})
		return nil
	}
	if !isRealPair(realMap, bq.VenueKey(), sq.VenueKey()) {
		rejects.Inc(rejects.Key{Face: "xasset_trypair", Gate: "is_real"})
		return nil
	}
	// 跟踪误差（§5.3）：分母=卖腿 mid（canonical_b 对应 sc）。
	// 交割配对豁免（D-649续③）：spot×futures 价差=基差机会本身非跟踪误差——
	// 套 tracking 闸等于把机会本身拒掉。
	isDeliv := be.VenueType == "futures" || se.VenueType == "futures"
	bm, sm := bq.Mid(), sq.Mid()
	if bm <= 0 || sm <= 0 {
		rejects.Inc(rejects.Key{Face: "xasset_trypair", Gate: "mid_nonpos"})
		return nil
	}
	if !isDeliv && math.Abs(bm-sm)/sm*10000 > d.TrackingMaxBps {
		rejects.Inc(rejects.Key{Face: "xasset_trypair", Gate: "tracking"})
		return nil
	}
	// 进场价差（一次性 bps）：与 evaluator S5 同口径 mid=(PriceBuy+PriceSell)/2。
	mid := (bq.Ask + sq.Bid) / 2
	if mid <= 0 {
		rejects.Inc(rejects.Key{Face: "xasset_trypair", Gate: "mid_nonpos"})
		return nil
	}
	if (sq.Bid-bq.Ask)/mid*10000 < d.EntryMinBps {
		rejects.Inc(rejects.Key{Face: "xasset_trypair", Gate: "entry_min"})
		return nil
	}
	// 净息差年化 pp：funding（perp 卖腿收/买腿付，缺 fact 不评估）+ MT 腿 swap（bps_daily→%/年=×365/100）。
	var net float64
	if se.VenueType == "perp" {
		f, ok := lookupFunding(funding, sc, se.Venue)
		if !ok {
			rejects.Inc(rejects.Key{Face: "xasset_trypair", Gate: "funding_absent"})
			return nil
		}
		net += f // 空腿收 funding（负 funding 自然变负项）
	}
	if be.VenueType == "perp" {
		f, ok := lookupFunding(funding, bc, be.Venue)
		if !ok {
			rejects.Inc(rejects.Key{Face: "xasset_trypair", Gate: "funding_absent"})
			return nil
		}
		net -= f // 多腿付 funding
	}
	net += swapAnnualPct(swap, bc, be, true)
	net += swapAnnualPct(swap, sc, se, false)
	// 交割腿基差年化 pp：卖 futures 收溢价收敛（premium=ask/bid 价差×365/
	// tenor）；买 futures 付溢价对称扣。tenor 缺=fail-closed（年化分母不可臆）。
	if se.VenueType == "futures" {
		if se.TenorDays <= 0 {
			rejects.Inc(rejects.Key{Face: "xasset_trypair", Gate: "tenor"})
			return nil
		}
		net += (sq.Bid - bq.Ask) / bq.Ask * 365 / se.TenorDays * 100
	}
	if be.VenueType == "futures" {
		if be.TenorDays <= 0 {
			rejects.Inc(rejects.Key{Face: "xasset_trypair", Gate: "tenor"})
			return nil
		}
		net -= (sq.Bid - bq.Ask) / bq.Ask * 365 / be.TenorDays * 100
	}
	if net < d.MinNetAnnualPct {
		rejects.Inc(rejects.Key{Face: "xasset_trypair", Gate: "net_min"})
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
