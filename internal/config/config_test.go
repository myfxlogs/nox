package config

import (
	"testing"
	"time"
)

// TestLoadDefaults 默认值须与 D-035 参数清单一致（禁止散落硬编码后，这里是唯一出处）。
func TestLoadDefaults(t *testing.T) {
	c := Load()
	if c.CrossVenueMinGrossBps != 10.0 {
		t.Errorf("CrossVenueMinGrossBps = %v, want 10.0", c.CrossVenueMinGrossBps)
	}
	if c.FundingMinNetAnnualized != 4.0 {
		t.Errorf("FundingMinNetAnnualized = %v, want 4.0", c.FundingMinNetAnnualized)
	}
	if c.CarryMinNetAnnualized != 4.0 {
		t.Errorf("CarryMinNetAnnualized = %v, want 4.0", c.CarryMinNetAnnualized)
	}
	if c.GrabMinNetAnnualized != 4.5 {
		t.Errorf("GrabMinNetAnnualized = %v, want 4.5", c.GrabMinNetAnnualized)
	}
	if c.SwapRefresh != time.Hour {
		t.Errorf("SwapRefresh = %v, want 1h", c.SwapRefresh)
	}
	if c.ScanThrottle != 30*time.Second {
		t.Errorf("ScanThrottle = %v, want 30s", c.ScanThrottle)
	}
	if c.SlippageBps != 1.0 {
		t.Errorf("SlippageBps = %v, want 1.0", c.SlippageBps)
	}
	if c.MarginRateAnnual != 4.5 {
		t.Errorf("MarginRateAnnual = %v, want 4.5", c.MarginRateAnnual)
	}
	if c.CloseBpsThr != 2.0 {
		t.Errorf("CloseBpsThr = %v, want 2.0", c.CloseBpsThr)
	}
	if c.CommissionCFDBps != 0.3 {
		t.Errorf("CommissionCFDBps = %v, want 0.3", c.CommissionCFDBps)
	}
	if c.CommissionCEXTakerBps != 10.0 {
		t.Errorf("CommissionCEXTakerBps = %v, want 10.0", c.CommissionCEXTakerBps)
	}
	if c.DetectorFreshnessTTL != 60*time.Second {
		t.Errorf("DetectorFreshnessTTL = %v, want 60s", c.DetectorFreshnessTTL)
	}
	if c.OpportunityTTL != 30*time.Second {
		t.Errorf("OpportunityTTL = %v, want 30s", c.OpportunityTTL)
	}
	if c.SymbolsTTL != 5*time.Minute {
		t.Errorf("SymbolsTTL = %v, want 5m", c.SymbolsTTL)
	}
	if c.LiqMMR != 0.005 {
		t.Errorf("LiqMMR = %v, want 0.005", c.LiqMMR)
	}
}

// TestLoadEnvOverride env 覆盖须生效。
func TestLoadEnvOverride(t *testing.T) {
	t.Setenv("ARB_CROSS_MIN_GROSS_BPS", "0.5")
	t.Setenv("ARB_COMMISSION_CFD_BPS", "0.6")
	t.Setenv("ARB_DETECTOR_FRESHNESS_TTL", "90s")
	t.Setenv("ARB_LIQ_MMR", "0.01")
	c := Load()
	if c.CrossVenueMinGrossBps != 0.5 {
		t.Errorf("CrossVenueMinGrossBps = %v, want 0.5", c.CrossVenueMinGrossBps)
	}
	if c.CommissionCFDBps != 0.6 {
		t.Errorf("CommissionCFDBps = %v, want 0.6", c.CommissionCFDBps)
	}
	if c.DetectorFreshnessTTL != 90*time.Second {
		t.Errorf("DetectorFreshnessTTL = %v, want 90s", c.DetectorFreshnessTTL)
	}
	if c.LiqMMR != 0.01 {
		t.Errorf("LiqMMR = %v, want 0.01", c.LiqMMR)
	}
	// 未设置的字段仍用默认
	if c.CarryMinNetAnnualized != 4.0 {
		t.Errorf("CarryMinNetAnnualized = %v, want default 4.0", c.CarryMinNetAnnualized)
	}
	if c.CommissionTaker["binance"] != 10.0 {
		t.Errorf("CommissionTaker[binance] = %v, want 10.0", c.CommissionTaker["binance"])
	}
}

// TestLoadTakerEnvOverride 逐交易所 taker env 覆盖。
func TestLoadTakerEnvOverride(t *testing.T) {
	t.Setenv("ARB_COMMISSION_TAKER_GATE", "5.0")
	c := Load()
	if c.CommissionTaker["gate"] != 5.0 {
		t.Errorf("CommissionTaker[gate] = %v, want 5.0", c.CommissionTaker["gate"])
	}
	if c.CommissionTaker["okx"] != 10.0 {
		t.Errorf("CommissionTaker[okx] = %v, want default 10.0", c.CommissionTaker["okx"])
	}
	if c.CommissionCEXTakerBps != 10.0 {
		t.Errorf("CommissionCEXTakerBps = %v, want default 10.0", c.CommissionCEXTakerBps)
	}
}

// TestLoadEnvInvalid 非法值回退默认（不崩溃）。
func TestLoadEnvInvalid(t *testing.T) {
	t.Setenv("ARB_SLIPPAGE_BPS", "not-a-number")
	t.Setenv("ARB_OPPORTUNITY_TTL", "xyz")
	c := Load()
	if c.SlippageBps != 1.0 {
		t.Errorf("SlippageBps = %v, want default 1.0", c.SlippageBps)
	}
	if c.OpportunityTTL != 30*time.Second {
		t.Errorf("OpportunityTTL = %v, want default 30s", c.OpportunityTTL)
	}
}
