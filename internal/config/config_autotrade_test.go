package config

import (
	"os"
	"testing"
)

// TestLoad_AutotradeDefaults D-072 默认值校验。
func TestLoad_AutotradeDefaults(t *testing.T) {
	os.Unsetenv("ARB_AUTOTRADE_ALLOW_VENUES")
	os.Unsetenv("ARB_AUTOTRADE_ALLOW_CANONICALS")
	os.Unsetenv("ARB_AUTOTRADE_EXCLUDE_CANONICALS")
	os.Unsetenv("ARB_MEANREVERT_THRESHOLDS")
	os.Unsetenv("ARB_AUTOTRADE_MIN_NET_SWAP_BPS")
	os.Unsetenv("ARB_SWAPARB_EXIT_NET_BPS")
	os.Unsetenv("ARB_FUNDING_EXIT_NET_PCT")

	c := Load()
	if !c.AutoTradeAllowVenues["277842155:mt5"] || !c.AutoTradeAllowVenues["REDACTED_MT_ACCT:mt5"] {
		t.Errorf("default AllowVenues missing: %v", c.AutoTradeAllowVenues)
	}
	if !c.AutoTradeAllowCanonicals["NAS100"] {
		t.Errorf("default AllowCanonicals missing NAS100: %v", c.AutoTradeAllowCanonicals)
	}
	if !c.AutoTradeExcludeCanonicals["BRENT"] || !c.AutoTradeExcludeCanonicals["WTI"] {
		t.Errorf("default ExcludeCanonicals missing: %v", c.AutoTradeExcludeCanonicals)
	}
	if c.MeanRevertThresholds["NAS100"] != 1.5 {
		t.Errorf("default MeanRevertThresholds NAS100 want 1.5, got %v", c.MeanRevertThresholds["NAS100"])
	}
	if c.AutoTradeMinNetSwapBps != 0.2 {
		t.Errorf("default MinNetSwapBps want 0.2, got %v", c.AutoTradeMinNetSwapBps)
	}
	if c.SwapArbExitNetBps != 0 {
		t.Errorf("default SwapArbExitNetBps want 0, got %v", c.SwapArbExitNetBps)
	}
	if c.FundingExitNetPct != 0 {
		t.Errorf("default FundingExitNetPct want 0, got %v", c.FundingExitNetPct)
	}
}

// TestLoad_AutotradeEnvOverride env 覆盖校验。
func TestLoad_AutotradeEnvOverride(t *testing.T) {
	t.Setenv("ARB_AUTOTRADE_ALLOW_VENUES", "111:mt5,222:mt5")
	t.Setenv("ARB_MEANREVERT_THRESHOLDS", "BTCUSDT:100,ETHUSDT:50")
	t.Setenv("ARB_AUTOTRADE_MIN_NET_SWAP_BPS", "0.5")

	c := Load()
	if !c.AutoTradeAllowVenues["111:mt5"] || !c.AutoTradeAllowVenues["222:mt5"] {
		t.Errorf("env AllowVenues override failed: %v", c.AutoTradeAllowVenues)
	}
	if c.AutoTradeAllowVenues["277842155:mt5"] {
		t.Error("default venue should be replaced, not merged")
	}
	if c.MeanRevertThresholds["BTCUSDT"] != 100 {
		t.Errorf("env MeanRevertThresholds override failed: %v", c.MeanRevertThresholds)
	}
	if c.AutoTradeMinNetSwapBps != 0.5 {
		t.Errorf("env MinNetSwapBps override failed: %v", c.AutoTradeMinNetSwapBps)
	}
}

// TestEnvStringSet 空字符串返回空 set。
func TestEnvStringSet(t *testing.T) {
	t.Setenv("ARB_TEST_EMPTY_SET", "")
	m := envStringSet("ARB_TEST_EMPTY_SET", "default1,default2")
	if len(m) != 2 {
		t.Errorf("empty env should fall back to default, got %v", m)
	}
}

// TestEnvFloatMap 空字符串返回空 map。
func TestEnvFloatMap(t *testing.T) {
	t.Setenv("ARB_TEST_EMPTY_MAP", "")
	m := envFloatMap("ARB_TEST_EMPTY_MAP", "A:1.0,B:2.0")
	if len(m) != 2 || m["A"] != 1.0 {
		t.Errorf("empty env should fall back to default, got %v", m)
	}
}
