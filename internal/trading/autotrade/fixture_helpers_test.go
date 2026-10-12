// fixture_helpers_test.go —— d335/d330 纯夹具（种库类 helper 不依赖 dashboard，D-212 拆搬保留；
// d335Engine/d330XAUService/d330Engine 依赖 dashboard.Service → 随挂起件 M2.5 回补）。
package autotrade

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/store/pgstore"
)

func d404SeedMonitorRowAt(t *testing.T, s *pgstore.Store, ts time.Time, entry, floor float64) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(), `
		INSERT INTO fin_xasset_monitor (ts, pair, binance_bid, binance_ask, exness_bid, exness_ask,
			entry_spread_bps, exit_spread_bps, entry_net_bps, funding_rate_8h, funding_annual_pct,
			tracking_error_bps, equiv_annual_pct, noise_bps, harvest_floor_bps, cex_venue, mt_venue, src)
		VALUES ($1, 'XPTUSDT-XPT', 4000, 4001, 3999, 4000, $2, 5, 3, 0.001, 21.9, 1.0, 24.9, 9, $3,
			'okx:perp','3001157:mt5','live')`, ts, entry, floor); err != nil {
		t.Fatalf("seed monitor row: %v", err)
	}
}

// d014FeeAny 宽松版费率 fake：MT 腿 0（佣金旗），CEX 腿 5bps——供非 econ 专题的
// 存量测试引擎装配 EconFeeBps（装配缺位=fail-closed 会拦死无关测试目标）。
func d014FeeAny(_ context.Context, venueKey string) (float64, bool) {
	if keydomain.IsMTKey(venueKey) {
		return 0, true
	}
	return 5.0, true
}
