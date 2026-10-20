package usdnotional

// D-633 USDPer A/B 档：计价币→USD 现态换算=qbus MT 键域（禁 PG——fail-closed）。
import (
	"context"
	"log"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/keydomain"
	"nox/internal/platform/notional"
)

// FxFreshTTL A 档鲜度窗（与 tickFreshWindow 同档 5min）。
const FxFreshTTL = 5 * time.Minute

// USDPerNow A 档：TTL=FxFreshTTL（5min）调单实现。
func USDPerNow(ctx context.Context, qbus *bus.QuoteBus, quoteCcy, preferVenueKey string) (float64, bool) {
	return USDPerNowTTL(ctx, qbus, quoteCcy, preferVenueKey, FxFreshTTL)
}

// USDPerNowTTL 单实现：qbus Snapshot→MT 键域（IsType mt4/mt5）→preferVenueKey
// 优先（须鲜度达标——陈帧落择鲜径，D1 返修）→miss 择 LatestQuote(ttl) 最新鲜
// 一家+venueKey 留痕→全 miss=fail-closed。
func USDPerNowTTL(ctx context.Context, qbus *bus.QuoteBus, quoteCcy, preferVenueKey string, ttl time.Duration) (float64, bool) {
	if quoteCcy == "USD" {
		return 1.0, true
	}
	pair, invert, ok := notional.FXPair(quoteCcy)
	if !ok || qbus == nil {
		return 0, false
	}
	venues := qbus.Snapshot(ctx, []string{pair})[pair]
	// MT 键域过滤（FX 直盘仅 MT 户在采——CEX 键不入）
	mt := make(map[string]bus.Quote)
	for vk, q := range venues {
		if keydomain.IsMTKey(vk) {
			mt[vk] = q
		}
	}
	if preferVenueKey != "" {
		if q, ok := mt[preferVenueKey]; ok && q.Bid > 0 && time.Since(q.Time) <= ttl {
			log.Printf("[fx] USDPerNow %s prefer %s bid=%v", pair, preferVenueKey, q.Bid)
			if invert {
				return 1.0 / q.Bid, true
			}
			return q.Bid, true
		}
	}
	if q, ok := bus.LatestQuote(mt, ttl, time.Now()); ok && q.Bid > 0 {
		log.Printf("[fx] USDPerNow %s 择鲜 venueKey=%s bid=%v", pair, q.VenueKey(), q.Bid)
		if invert {
			return 1.0 / q.Bid, true
		}
		return q.Bid, true
	}
	return 0, false
}

// FxLive exec.FxResolver 实现（A 档——USDPerNow 唯一径）。
type FxLive struct{ Bus *bus.QuoteBus }

func NewFxLive(qbus *bus.QuoteBus) *FxLive { return &FxLive{Bus: qbus} }

// USDPerNow exec.FxResolver 接口（D-633 A 档现态径）。
func (f *FxLive) USDPerNow(ctx context.Context, quoteCcy, preferVenueKey string) (float64, bool) {
	return USDPerNow(ctx, f.Bus, quoteCcy, preferVenueKey)
}
