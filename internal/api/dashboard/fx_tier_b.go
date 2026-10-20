package dashboard

// D-633 B 档：USDPer 估值/敞口（D-634 显式例外仅此四处）——
// qbus USDPerNowTTL 24h→miss 落 PG 末帧标 stale（禁静默顶替：stale 注记如实）。
import (
	"context"
	"time"

	"nox/internal/platform/usdnotional"
)

const fxStaleTTL = 24 * time.Hour

// usdPerOrStale 返回 (rate, stale, ok)——stale=true=PG 末帧兜底（估值/敞口域）。
func (s *Service) usdPerOrStale(ctx context.Context, ccy, preferVenueKey string, at time.Time) (float64, bool, bool) {
	if rate, ok := usdnotional.USDPerNowTTL(ctx, s.qbus, ccy, preferVenueKey, fxStaleTTL); ok && rate > 0 {
		return rate, false, true
	}
	if rate, ok := s.store.USDPer(ctx, ccy, at, preferVenueKey); ok && rate > 0 {
		return rate, true, true // PG 末帧=stale（D-634 例外域）
	}
	return 0, false, false
}
