// venue_is_real_test.go —— D-311 S8锚⑨ venue is_real 合并视图闸
// （d311_xasset_test 拆出，D-212 拆搬；gate 非导出方法同包可及）。
package gate

import (
	"context"
	"testing"

	"arb/internal/store/pgstore"
)

func d311Truncate(t *testing.T, s *pgstore.Store) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(),
		`TRUNCATE facts, positions, position_legs, opportunities, broker_symbol_map, broker_accounts, exchange_configs, autotrade_logs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// TestD311_VenueIsRealGate_CEXAndMT S8锚⑨：ListVenueIsReal 合并视图——
// CEX venue（exchange_configs.simulated）开始可判；MT（broker_accounts）行为回归不变。
func TestD311_VenueIsRealGate_CEXAndMT(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	// MT demo 账户 + CEX：gate=实盘（simulated=f）、okx=模拟（simulated=t）。
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, is_real)
		 VALUES ('vD', 'mt5', 'h', 443, 'p', 's', 'connected', false)`); err != nil {
		t.Fatalf("seed broker: %v", err)
	}
	for _, row := range []struct {
		venue     string
		simulated bool
	}{
		{"gate", false}, // → is_real=true
		{"okx", true},   // → is_real=false
	} {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO exchange_configs (venue, venue_type, api_key, api_secret, simulated)
			 VALUES ($1, 'perp', 'k', 's', $2)`, row.venue, row.simulated); err != nil {
			t.Fatalf("seed exchange %s: %v", row.venue, err)
		}
	}
	// env=demo：demo 腿（vD:mt5 + okx:perp）过；实盘腿 gate:perp 拒。
	g := &Gate{store: s, cfg: Config{Environment: "demo"}}
	if v := g.environmentIsRealCheck(ctx, TradeIntent{VenueBuy: "vD:mt5", VenueSell: "okx:perp"}); !v.Pass {
		t.Errorf("demo venues env=demo: want pass, got %s", v.Reason)
	}
	if v := g.environmentIsRealCheck(ctx, TradeIntent{VenueBuy: "vD:mt5", VenueSell: "gate:perp"}); v.Pass {
		t.Error("real CEX venue env=demo: want reject, got pass")
	}
	// env=live：gate:perp（真实盘）过。
	gLive := &Gate{store: s, cfg: Config{Environment: "live"}}
	if v := gLive.environmentIsRealCheck(ctx, TradeIntent{VenueBuy: "gate:perp", VenueSell: "gate:perp"}); !v.Pass {
		t.Errorf("real CEX venue env=live: want pass, got %s", v.Reason)
	}
	// liveGate（ARB_LIVE_TRADE=false）：gate:perp 实盘拦；okx:perp 模拟放行；vD:mt5 demo 放行（MT 回归）。
	gl := &Gate{store: s, cfg: Config{}}
	if v := gl.liveGateCheck(ctx, TradeIntent{VenueBuy: "gate:perp", VenueSell: "gate:perp"}); v.Pass {
		t.Error("liveGate real CEX: want reject, got pass")
	}
	if v := gl.liveGateCheck(ctx, TradeIntent{VenueBuy: "okx:perp", VenueSell: "vD:mt5"}); !v.Pass {
		t.Errorf("liveGate demo venues: want pass, got %s", v.Reason)
	}
	// 未知 venue fail-closed（MT 回归语义不变）。
	if v := gl.liveGateCheck(ctx, TradeIntent{VenueBuy: "ghost:mt5", VenueSell: "vD:mt5"}); v.Pass {
		t.Error("unknown venue: want reject (fail-closed), got pass")
	}
}
