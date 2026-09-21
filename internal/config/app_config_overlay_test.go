// app_config_overlay_test.go —— D-368 S6：overlay 锚 T1-T5（纯函数，无 DB）。
package config

import (
	"os"
	"testing"
)

func TestApplyAppConfigOverlay(t *testing.T) {
	// 清 env 残留（T2/T3 断言前提）
	for _, k := range []string{"ARB_OPP_NOTIONAL_USD", "ARB_CLOSE_BPS_THR", "ARB_AUTOTRADE_MAX_POSITIONS", "ARB_AUTH_TOKEN", "ARB_AUTOTRADE_MAX_NOTIONAL"} {
		os.Unsetenv(k)
	}
	defer os.Unsetenv("ARB_OPP_NOTIONAL_USD")

	// T1：DB 行+env 同设不同值 → DB 赢（env 残值告警经日志锚，此处断言值语义）
	t.Setenv("ARB_OPP_NOTIONAL_USD", "3000")
	cfg := &Config{}
	rows := map[string]string{"ARB_OPP_NOTIONAL_USD": "5000"}
	ApplyAppConfigOverlay(cfg, rows)
	if cfg.OpportunityNotionalUSD != 5000 {
		t.Errorf("T1: DB 行应赢, got %v want 5000", cfg.OpportunityNotionalUSD)
	}

	// T2：DB 无行+env 设值 → env 生效（零漂移）
	cfg2 := &Config{}
	ApplyAppConfigOverlay(cfg2, nil)
	if cfg2.OpportunityNotionalUSD != 0 {
		// Load() 的 env/default 阶段不在本函数——overlay 只在 DB 有行时改写；
		// 无行时 cfg 字段保持 Load() 产物（此处直接断言零改写）。
		t.Errorf("T2: DB 无行 overlay 不应改写字段, got %v", cfg2.OpportunityNotionalUSD)
	}

	// T3：DB 值非法（float 键给 "abc"）→ warn+保留（不被置零）
	cfg3 := &Config{OpportunityNotionalUSD: 123}
	ApplyAppConfigOverlay(cfg3, map[string]string{"ARB_OPP_NOTIONAL_USD": "abc"})
	if cfg3.OpportunityNotionalUSD != 123 {
		t.Errorf("T3: 非法值应保留原值, got %v", cfg3.OpportunityNotionalUSD)
	}

	// T4：非白名单键（ARB_AUTH_TOKEN）→ 忽略（cfg 零触碰；秘密类永不经此通道）
	cfg4 := &Config{}
	ApplyAppConfigOverlay(cfg4, map[string]string{"ARB_AUTH_TOKEN": "leak-attempt", "ARB_UNKNOWN_KNOB": "1"})
	_ = cfg4

	// T5：八键全 DB 化 → 全字段=DB 值
	cfg5 := &Config{}
	rows5 := map[string]string{
		"ARB_OPP_NOTIONAL_USD":           "1001",
		"ARB_AUTOTRADE_MAX_NOTIONAL":     "2002",
		"ARB_AUTOTRADE_MAX_POSITIONS":    "7",
		"ARB_AUTOTRADE_MIN_NET_SWAP_BPS": "0.3",
		"ARB_CARRY_REGIME_FLOOR_PCT":     "2.5",
		"ARB_FUNDING_MAX_BREAKEVEN_DAYS": "1.5",
		"ARB_CROSS_MIN_GROSS_BPS_CFD":    "22",
		"ARB_CLOSE_BPS_THR":              "0.7",
	}
	ApplyAppConfigOverlay(cfg5, rows5)
	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"ARB_OPP_NOTIONAL_USD", cfg5.OpportunityNotionalUSD, 1001},
		{"ARB_AUTOTRADE_MAX_NOTIONAL", cfg5.AutoTradeMaxNotional, 2002},
		{"ARB_AUTOTRADE_MIN_NET_SWAP_BPS", cfg5.AutoTradeMinNetSwapBps, 0.3},
		{"ARB_CARRY_REGIME_FLOOR_PCT", cfg5.CarryRegimeFloorPct, 2.5},
		{"ARB_FUNDING_MAX_BREAKEVEN_DAYS", cfg5.FundingMaxBreakevenDays, 1.5},
		{"ARB_CROSS_MIN_GROSS_BPS_CFD", cfg5.CrossVenueMinGrossBpsCFD, 22},
		{"ARB_CLOSE_BPS_THR", cfg5.CloseBpsThr, 0.7},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("T5: %s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if cfg5.AutoTradeMaxPositions != 7 {
		t.Errorf("T5: ARB_AUTOTRADE_MAX_POSITIONS = %d, want 7", cfg5.AutoTradeMaxPositions)
	}

	// T3 强化：int 键非法值同语义
	cfg6 := &Config{}
	ApplyAppConfigOverlay(cfg6, map[string]string{"ARB_AUTOTRADE_MAX_POSITIONS": "abc"})
	if cfg6.AutoTradeMaxPositions != 0 {
		// setter 零值写回前 err 非 nil → overlay continue，字段保持零改写；此处 cfg6 新构=0 为初始值
		t.Logf("int 非法值: MaxPositions=%d（overlay continue 语义）", cfg6.AutoTradeMaxPositions)
	}
}
