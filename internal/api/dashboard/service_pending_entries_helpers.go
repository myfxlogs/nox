// service_pending_entries_helpers.go D-497 行数折分（自 service_pending_entries.go 搬出，
// 纯搬家零逻辑变更）：comboTier 安全档位判定 + venueExposureSum + monitor/leg/venue 键帮手。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/portfolio/capitalview"
	"nox/internal/store/pgstore"

	v1 "nox/internal/gen/dashboardv1"
)

// comboTier D-478 S4→D-010 S4：安全档位（服务端算，前端只渲染）——裸腿名义顶。
// needed=单腿名义 N：notional_usd（NULL→MaxGrossUSD/2 回退）；margin_usd 模式
// N=margin÷(fracB+fracS)（杠杆实读败→missing_data 不猜）；仍缺→unset。
// vs 腿 venue 权益×crossCap；
// 权益源=capitalview.Live 统一读口（D-549：活账本→REST 校准→快照兜底带龄注记，
// 「快照+5min 墙钟窗」判定根除——D-542 漏改点的 missing_data flapping 根修）；任一腿超档即 warn。
func (s *Service) comboTier(ctx context.Context, c pgstore.XAssetComboAdminRow, snaps []pgstore.VenueAccountLatest) (tier, detail string, warn bool) {
	needed := 0.0
	switch {
	case c.MarginUSD != nil:
		n, _, _, _, _, ok := s.comboLegNotional(ctx, c)
		if !ok {
			return "missing_data", "无法判定：杠杆实读失败保证金不可折算（margin 预算模式需双腿杠杆实读）", false
		}
		needed = n
	case c.NotionalUSD != nil:
		needed = *c.NotionalUSD
	case c.MaxGrossUSD > 0:
		needed = c.MaxGrossUSD / 2
	default:
		return "unset", "未设仓位上限", false
	}
	cap := s.crossCap
	if cap <= 0 {
		cap = defaultCrossCap
	}
	for _, leg := range []struct{ venue string }{{c.VenueBuy}, {c.VenueSell}} {
		bv := bareVenueKey(leg.venue)
		f, found := s.capitalView().LiveSnaps(ctx, leg.venue, snaps)
		if !found || f.Equity <= 0 {
			return "missing_data", fmt.Sprintf("无法判定：账户 %s 暂无资金数据（活账本/REST/快照三径全空）", bv), false
		}
		src := ""
		if f.Src == capitalview.SrcSnap {
			src = "，" + f.Note // 快照兜底龄注记透出（禁静默）
		}
		if needed > f.Equity*cap {
			return "over", fmt.Sprintf("超出安全线：计划仓位 $%.0f > 账户 %s 净值 $%.0f 的 %.1f 倍上限%s", needed, bv, f.Equity, cap, src), true
		}
	}
	return "ok", fmt.Sprintf("在承受力内：计划仓位 $%.0f ≤ 账户净值 × %.1f 倍上限", needed, cap), false
}

// venueMarginSum/comboQtyBelowMin/minOpenableUSD 已迁/重写（D-010：venue 实占口径翻
// 保证金 + qty 明示 S0 2×误拦回归修复）——见 service_venue_system_exposure.go /
// service_pending_margin.go。

// monitorPairKey / monitorLegKeys paper_all_in.go 同式镜像（pair=cexCanon+"-"+bare(mtCanon)）。
func monitorPairKey(c pgstore.XAssetComboAdminRow) string {
	mtCanon := c.CanonicalBuy
	cexCanon := c.CanonicalSell
	if keydomain.IsMTKey(c.VenueSell) {
		mtCanon, cexCanon = c.CanonicalSell, c.CanonicalBuy
	}
	bare := strings.TrimSuffix(mtCanon, "USDT")
	if bare == mtCanon {
		bare = strings.TrimSuffix(mtCanon, "USD")
	}
	return cexCanon + "-" + bare
}

func monitorLegKeys(c pgstore.XAssetComboAdminRow) (cexVenue, mtVenue string) {
	if keydomain.IsMTKey(c.VenueSell) {
		return legBare(c.VenueBuy, "perp"), legBare(c.VenueSell, "mt5")
	}
	return legBare(c.VenueSell, "perp"), legBare(c.VenueBuy, "mt5")
}

// legBare D-563 S2：带域键即监控表键原样返回（剥域=查裸键恒 miss 假阳性实证）；
// 裸 venue → 补域后缀。
func legBare(venueKey, suffix string) string {
	if venueKey == "" || strings.Contains(venueKey, ":") {
		return venueKey
	}
	return keydomain.Join(venueKey, suffix)
}

// legMarket 腿市场形态 → (venue_type, symbol)：MT 腿 ticks=mt4/mt5+canonical；
// cex perp/spot 腿 symbol=canonical（tick 源=采集器原生符号，缺=不设闸）。
func legMarket(venueKey, canonical string) (vt, sym string) {
	switch {
	case keydomain.IsType(venueKey, "mt5"):
		return "mt5", canonical
	case keydomain.IsType(venueKey, "mt4"):
		return "mt4", canonical
	case keydomain.IsType(venueKey, "perp"):
		return "perp", canonical
	case keydomain.IsType(venueKey, "spot"):
		return "spot", canonical
	}
	return "", ""
}

// exposureCapsGate D-006 S3→D-010 S2：闸 6（venue 保证金占用 cap 前瞻化）+闸 6.5
// （canonical 聚簇 cap，名义口径不动=裸腿风险域）。
// venue 判据：实占保证金（venueMarginSum=Σ OPEN 腿 名义×legMarginNeedFrac）+
// 本次该腿保证金 N×frac(v) > cap 拒；frac 实读败→该腿按名义全款计（frac=1 保守）；
// margin_usd 模式 frac 缺一→整组合不可折算，fail-closed 拒。
// 475a 三态同判据：cap=0 冻结 / 前瞻拒。闸 6.5：同标的多仓同向基差聚簇实证（#11/#12
// XAUUSD）；canonical 买卖同名单判，proposed 口径同前（notional??gross）。返回 (拒因,false)/("",true)。
func (s *Service) exposureCapsGate(ctx context.Context, c pgstore.XAssetComboAdminRow, caps map[string]pgstore.VenueExposureCap) (string, bool) {
	proposed := c.MaxGrossUSD
	if c.NotionalUSD != nil {
		proposed = *c.NotionalUSD
	}
	legN, fracB, fracS, knownB, knownS, ok := s.comboLegNotional(ctx, c)
	if c.MarginUSD != nil {
		if !ok {
			return "杠杆实读失败保证金不可折算（margin 预算模式需双腿杠杆实读，fail-closed 不放行）", false
		}
		proposed = legN // canonical 聚簇 cap 名义口径：margin 模式本次名义=反推单腿 N
	}
	legs := []struct {
		vk    string
		frac  float64
		known bool
	}{{c.VenueBuy, fracB, knownB}, {c.VenueSell, fracS, knownS}}
	for _, leg := range legs { // D-007 S2：typed 键直查（剥 bare 恒 miss 死键已修）
		v := leg.vk
		if capRow, ok := caps[v]; ok && capRow.Enabled {
			if capRow.CapUSD == 0 {
				return fmt.Sprintf("保证金占用冻结：账户 %s 上限设为0，暂停新入场", v), false
			}
			sum, err := s.VenueSystemMarginUSD(ctx, keydomain.BareOf(v))
			if err != nil {
				return fmt.Sprintf("保证金实占查询失败（保守不放行）: 账户 %s %v", v, err), false
			}
			frac := leg.frac
			if !leg.known {
				frac = 1.0 // frac 不可得→名义全款计（保守不放行，D-010 边界）
			}
			prop := legN * frac
			if sum+prop > capRow.CapUSD {
				return fmt.Sprintf("保证金占用超限：账户 %s 实占 $%.0f+本次 $%.0f 超过上限 $%.0f", v, sum, prop, capRow.CapUSD), false
			}
		}
	}
	canons := []string{c.CanonicalBuy}
	if c.CanonicalSell != c.CanonicalBuy {
		canons = append(canons, c.CanonicalSell)
	}
	for _, canon := range canons {
		if r := s.store.CanonicalCapReject(ctx, canon, proposed); r != "" {
			return r, false
		}
	}
	return "", true
}

// fillWaitCondition D-580：XASSET_CARRY 七闸全过时 WaitReason 人话整串+三字段透出。
func (s *Service) fillWaitCondition(ctx context.Context, c pgstore.XAssetComboAdminRow, row *v1.XAssetPendingEntryRow, ready bool) {
	if !ready || row.EntryKind != "XASSET_CARRY" {
		return
	}
	if cur, thr, ok, reason := s.waitEntryBps(ctx, c); ok {
		gap := thr - cur
		row.WaitReason = fmt.Sprintf("入场价差>%.1f｜当前%.1f｜还差%.1fbps", thr, cur, gap) // D-587 坑3：短格式（列头已含「等待」前缀）
		row.WaitCurrentBps, row.WaitThresholdBps, row.WaitGapBps = &cur, &thr, &gap
	} else if reason != "" {
		row.WaitReason += "（实况价差不可得：" + reason + "）" // D-006 S4：带因不黑话
	}
}

// waitEntryBps D-580：待入场等待条件实况——展示值与决策面（engine_opp 安全边际闸）
// 同公式同源：entryBps=(cexBid−mtAsk)/mtAsk×1e4 双腿 qbus 实时现算；
// 阈值=LatestXAssetEntryGate noise_bps（同接口同窗）。
// D-006 S4：ok=false 时带 reason——MT腿反向构型 / qbus未接{venue}:{symbol} /
// {venue}:{symbol}报价超龄{min}分钟 / monitor行缺 / 查询失败:{err}，排障信息全透出。
func (s *Service) waitEntryBps(ctx context.Context, c pgstore.XAssetComboAdminRow) (cur, thr float64, ok bool, reason string) {
	if !keydomain.IsMTKey(c.VenueBuy) {
		return 0, 0, false, "MT腿反向构型"
	}
	if s.qbus == nil {
		return 0, 0, false, "qbus未接" + c.CanonicalBuy
	}
	cexV, mtV := monitorLegKeys(c)
	_, noiseBps, _, _, gateOK, err := s.store.LatestXAssetEntryGate(ctx, monitorPairKey(c), cexV, mtV, monitorFreshWindow)
	if err != nil {
		return 0, 0, false, fmt.Sprintf("查询失败:%v", err)
	}
	if !gateOK {
		return 0, 0, false, "monitor行缺"
	}
	mtQ, ok1 := s.qbus.Latest(c.CanonicalBuy, c.VenueBuy)
	cexQ, ok2 := s.qbus.Latest(c.CanonicalSell, c.VenueSell)
	if !ok1 || mtQ.Ask <= 0 {
		return 0, 0, false, "qbus未接" + c.VenueBuy + ":" + c.CanonicalBuy
	}
	if !ok2 || cexQ.Bid <= 0 {
		return 0, 0, false, "qbus未接" + c.VenueSell + ":" + c.CanonicalSell
	}
	if age := time.Since(mtQ.Time); age > tickFreshWindow {
		return 0, 0, false, fmt.Sprintf("%s:%s报价超龄%.0f分钟", c.VenueBuy, c.CanonicalBuy, age.Minutes())
	}
	if age := time.Since(cexQ.Time); age > tickFreshWindow {
		return 0, 0, false, fmt.Sprintf("%s:%s报价超龄%.0f分钟", c.VenueSell, c.CanonicalSell, age.Minutes())
	}
	return (cexQ.Bid - mtQ.Ask) / mtQ.Ask * 1e4, noiseBps, true, ""
}

// bareVenueKey venue 键剥 :后缀（flags/caps/快照 bare 域对齐 engine bareVenue）。
func bareVenueKey(venueKey string) string {
	return keydomain.BareOf(venueKey)
}

// pendingInputs D-007 S1：pending 评估批量输入（RPC 与闸态 diff 器共用单源，P3）。
type pendingInputs struct {
	venueAllow, canonAllow map[string]bool
	flags                  map[string]pgstore.VenueSymbolFlag
	caps                   map[string]pgstore.VenueExposureCap
	snaps                  []pgstore.VenueAccountLatest
	active                 []pgstore.Position
	rejects                map[string]string
}

// loadPendingInputs 批量取数（禁 N+1）；失败 fail-closed 整体拒。
func (s *Service) loadPendingInputs(ctx context.Context) (*pendingInputs, error) {
	in := &pendingInputs{}
	var err error
	if in.active, err = s.store.ListActivePositions(ctx); err != nil {
		return nil, connectErr(err)
	}
	rules, err := s.store.ListAutotradeAllowRulesAll(ctx)
	if err != nil {
		return nil, connectErr(err)
	}
	in.venueAllow, in.canonAllow = map[string]bool{}, map[string]bool{}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if r.Kind == "venue" {
			in.venueAllow[r.Value] = true
		}
		if r.Kind == "canonical" {
			in.canonAllow[r.Value] = true
		}
	}
	if in.flags, err = s.store.ListActiveFlags(ctx); err != nil {
		return nil, connectErr(err)
	}
	if in.caps, err = s.store.ListVenueExposureCaps(ctx); err != nil {
		return nil, connectErr(err)
	}
	if in.snaps, err = s.liveAccountLatest(ctx); err != nil {
		return nil, connectErr(err)
	}
	in.rejects = s.fundingRejectFacts(ctx) // D-560 S2b：近6h资金拒批量透出（禁N+1）
	return in, nil
}
