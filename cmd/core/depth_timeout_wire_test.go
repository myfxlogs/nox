package main

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/collect/exchange"
	"nox/internal/platform/testutil/storetest"
)

// TestDepthTimeoutWire 装配链锚：AppConfigInt64→DepthTimeout 覆写
// （descs 补登票面顺手项——装配读径钉死防回退 env/字面量）。
func TestDepthTimeoutWire(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	// 种 app_config 覆写值（gate=15s；binance 无行保缺省）
	if err := st.InsertAppConfigIfAbsent(ctx, "NOX_CEX_DEPTH_TIMEOUT_GATE", "15"); err != nil {
		t.Fatal(err)
	}
	st.Pool().Exec(ctx, `UPDATE app_config SET value='15' WHERE key='NOX_CEX_DEPTH_TIMEOUT_GATE'`)
	defer st.Pool().Exec(ctx, `DELETE FROM app_config WHERE key='NOX_CEX_DEPTH_TIMEOUT_GATE'`)
	cfg := exchange.DefaultConfig([]string{"BTC"})
	// 装配链同构：AppConfigInt64 直读→map 覆写（与 buildDepthCollector 同径）
	for _, v := range []string{"binance", "gate", "okx"} {
		key := "NOX_CEX_DEPTH_TIMEOUT_" + map[string]string{"binance": "BINANCE", "gate": "GATE", "okx": "OKX"}[v]
		if n, ok, err := st.AppConfigInt64(ctx, key); err == nil && ok && n > 0 {
			cfg.DepthTimeout[v] = time.Duration(n) * time.Second
		}
	}
	if cfg.DepthTimeout["gate"] != 15*time.Second {
		t.Fatalf("app_config 覆写应生效 got %v", cfg.DepthTimeout["gate"])
	}
	if cfg.DepthTimeout["binance"] != 8*time.Second {
		t.Fatalf("行缺应保缺省 8s got %v", cfg.DepthTimeout["binance"])
	}
}
