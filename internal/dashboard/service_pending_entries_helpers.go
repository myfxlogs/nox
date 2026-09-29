// service_pending_entries_helpers.go D-497 行数折分（自 service_pending_entries.go 搬出，
// 纯搬家零逻辑变更）：comboTier 安全档位判定 + venueExposureSum + monitor/leg/venue 键帮手。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/capitalview"
	"arb/internal/keydomain"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"
)

// comboTier D-478 S4：安全档位（服务端算，前端只渲染）。
// needed=notional_usd（NULL→MaxGrossUSD 回退，仍缺→unset）；vs 腿 venue 权益×crossCap；
// 权益源=capitalview.Live 统一读口（D-549：活账本→REST 校准→快照兜底带龄注记，
// 「快照+5min 墙钟窗」判定根除——D-542 漏改点的 missing_data flapping 根修）；任一腿超档即 warn。
func (s *Service) comboTier(ctx context.Context, c pgstore.XAssetComboAdminRow, snaps []pgstore.VenueAccountLatest) (tier, detail string, warn bool) {
	needed := 0.0
	switch {
	case c.NotionalUSD != nil:
		needed = *c.NotionalUSD
	case c.MaxGrossUSD > 0:
		needed = c.MaxGrossUSD
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

// venueExposureSum 475a 同判据的 venue 已挂名义 Σ（enabled 组合 notional/MaxGross 回退）。
// err 传播=调用方 fail-closed 转「cap 查询失败」reason（R3：原吞 err 返 0=fail-open 误导）。
func (s *Service) venueExposureSum(venue string) (float64, error) {
	combos, err := s.store.ListXAssetAllowCombosAll(context.Background())
	if err != nil {
		return 0, err
	}
	sum := 0.0
	for _, c := range combos {
		if !c.Enabled {
			continue
		}
		if bareVenueKey(c.VenueBuy) != venue && bareVenueKey(c.VenueSell) != venue {
			continue
		}
		if c.NotionalUSD != nil {
			sum += *c.NotionalUSD
		} else {
			sum += c.MaxGrossUSD
		}
	}
	return sum, nil
}

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

// fillWaitCondition D-580：XASSET_CARRY 七闸全过时 WaitReason 人话整串+三字段透出。
func (s *Service) fillWaitCondition(ctx context.Context, c pgstore.XAssetComboAdminRow, row *v1.XAssetPendingEntryRow, ready bool) {
	if !ready || row.EntryKind != "XASSET_CARRY" {
		return
	}
	if cur, thr, ok := s.waitEntryBps(ctx, c); ok {
		gap := thr - cur
		row.WaitReason = fmt.Sprintf("等待：入场价差 > %.1fbps（24h摆幅）｜当前 %.1fbps｜还差 %.1fbps", thr, cur, gap)
		row.WaitCurrentBps, row.WaitThresholdBps, row.WaitGapBps = &cur, &thr, &gap
	} else {
		row.WaitReason += "（实况价差不可得）" // 诚实不猜（报价缺/超龄/monitor 无行）
	}
}

// waitEntryBps D-580：待入场等待条件实况——展示值与决策面（engine_opp 安全边际闸）
// 同公式同源：entryBps=(cexBid−mtAsk)/mtAsk×1e4 双腿 qbus 实时现算；
// 阈值=LatestXAssetEntryGate noise_bps（同接口同窗）。反向构型（MT 卖腿）monitor
// 方向未定义（engine 拒开同判）/qbus 未接线/报价缺或超 5min/monitor 无行或查询败
// → ok=false，调用方留空+诚实后缀不猜。
func (s *Service) waitEntryBps(ctx context.Context, c pgstore.XAssetComboAdminRow) (cur, thr float64, ok bool) {
	if !keydomain.IsMTKey(c.VenueBuy) || s.qbus == nil {
		return 0, 0, false
	}
	cexV, mtV := monitorLegKeys(c)
	_, noiseBps, _, _, gateOK, err := s.store.LatestXAssetEntryGate(ctx, monitorPairKey(c), cexV, mtV, monitorFreshWindow)
	if err != nil || !gateOK {
		return 0, 0, false
	}
	mtQ, ok1 := s.qbus.Latest(c.CanonicalBuy, c.VenueBuy)
	cexQ, ok2 := s.qbus.Latest(c.CanonicalSell, c.VenueSell)
	if !ok1 || !ok2 || mtQ.Ask <= 0 || cexQ.Bid <= 0 ||
		time.Since(mtQ.Time) > tickFreshWindow || time.Since(cexQ.Time) > tickFreshWindow {
		return 0, 0, false
	}
	return (cexQ.Bid - mtQ.Ask) / mtQ.Ask * 1e4, noiseBps, true
}

// bareVenueKey venue 键剥 :后缀（flags/caps/快照 bare 域对齐 engine bareVenue）。
func bareVenueKey(venueKey string) string {
	return keydomain.BareOf(venueKey)
}
