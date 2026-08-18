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
	if c.CarryMinNetBps != 8.0 {
		t.Errorf("CarryMinNetBps = %v, want 8.0", c.CarryMinNetBps)
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
}

// TestLoadEnvOverride env 覆盖须生效。
func TestLoadEnvOverride(t *testing.T) {
	t.Setenv("ARB_CROSS_MIN_GROSS_BPS", "0.5")
	t.Setenv("ARB_COMMISSION_CFD_BPS", "0.6")
	t.Setenv("ARB_DETECTOR_FRESHNESS_TTL", "90s")
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
	// 未设置的字段仍用默认
	if c.CarryMinNetBps != 8.0 {
		t.Errorf("CarryMinNetBps = %v, want default 8.0", c.CarryMinNetBps)
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
