// service_pending_entries_helpers.go D-497 行数折分（自 service_pending_entries.go 搬出，
// 纯搬家零逻辑变更）：comboTier 安全档位判定 + venueExposureSum + monitor/leg/venue 键帮手。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
)

// comboTier D-478 S4：安全档位（服务端算，前端只渲染）。
// needed=notional_usd（NULL→MaxGrossUSD 回退，仍缺→unset）；vs 腿 venue 权益×crossCap；
// 权益源=fin_venue_account_snapshot 最新行（>5min=缺数据不猜）；任一腿超档即 warn。
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
		v := bareVenueKey(leg.venue)
		eq, at, found := venueEquity(snaps, v)
		if !found || time.Since(at) > snapshotStaleWindow || eq <= 0 {
			return "missing_data", fmt.Sprintf("无法判定：账户 %s 暂无资金数据（未采集到净值）", v), false
		}
		if needed > eq*cap {
			return "over", fmt.Sprintf("超出安全线：计划仓位 $%.0f > 账户 %s 净值 $%.0f 的 %.1f 倍上限", needed, v, eq, cap), true
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

func legBare(venueKey, suffix string) string {
	if strings.Contains(venueKey, ":") {
		return keydomain.BareOf(venueKey)
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

// bareVenueKey venue 键剥 :后缀（flags/caps/快照 bare 域对齐 engine bareVenue）。
func bareVenueKey(venueKey string) string {
	return keydomain.BareOf(venueKey)
}

// venueEquity 快照按 bare venue 取权益（优先 perp/unified 行）。
func venueEquity(snaps []pgstore.VenueAccountLatest, venue string) (eq float64, at time.Time, ok bool) {
	for _, r := range snaps {
		if r.Venue != venue {
			continue
		}
		if r.EquityRaw == nil {
			continue
		}
		if r.VenueType == "perp" || r.VenueType == "unified" {
			return *r.EquityRaw, r.FetchedAt, true
		}
	}
	for _, r := range snaps {
		if r.Venue == venue && r.EquityRaw != nil {
			return *r.EquityRaw, r.FetchedAt, true
		}
	}
	return 0, time.Time{}, false
}
