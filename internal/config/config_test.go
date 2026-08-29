package config

import (
	"testing"
	"time"
)

// TestLoadDefaults 默认值须与 D-035 参数清单一致（禁止散落硬编码后，这里是唯一出处）。
func TestLoadDefaults(t *testing.T) {
	c := Load()
	if c.CrossVenueMinGrossBpsCEX != 10.0 {
		t.Errorf("CrossVenueMinGrossBpsCEX = %v, want 10.0", c.CrossVenueMinGrossBpsCEX)
	}
	if c.CrossVenueMinGrossBpsCFD != 20.0 {
		t.Errorf("CrossVenueMinGrossBpsCFD = %v, want 20.0 (D-078 round-trip)", c.CrossVenueMinGrossBpsCFD)
	}
	if c.FundingMinNetAnnualized != 4.0 {
		t.Errorf("FundingMinNetAnnualized = %v, want 4.0", c.FundingMinNetAnnualized)
	}
	if c.FundingBaselinePct != 10.95 {
		t.Errorf("FundingBaselinePct = %v, want 10.95 (D-084)", c.FundingBaselinePct)
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
	if c.SlippageBps != 0.3 {
		t.Errorf("SlippageBps = %v, want 0.3 (D-070)", c.SlippageBps)
	}
	if c.MarginRateAnnual != 4.5 {
		t.Errorf("MarginRateAnnual = %v, want 4.5", c.MarginRateAnnual)
	}
	if c.CloseBpsThr != 0.5 {
		t.Errorf("CloseBpsThr = %v, want 0.5 (D-070)", c.CloseBpsThr)
	}
	if c.CommissionCFDBps != 0.3 {
		t.Errorf("CommissionCFDBps = %v, want 0.3", c.CommissionCFDBps)
	}
	if c.CommissionCEXTakerBps != 5.0 {
		t.Errorf("CommissionCEXTakerBps = %v, want 5.0 (D-145 VIP0 taker 实测)", c.CommissionCEXTakerBps)
	}
	if c.DetectorFreshnessTTL != 120*time.Second {
		t.Errorf("DetectorFreshnessTTL = %v, want 120s (D-070)", c.DetectorFreshnessTTL)
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
	// 修复⑭ S3：NotifyMinNetBps 字段已退役（oppTopic 全量事件流），不再断言。
	if c.StoreMinNetBps != 5.0 {
		t.Errorf("StoreMinNetBps = %v, want 5.0 (D-078)", c.StoreMinNetBps)
	}
	if c.FundingHedgeMinNetStore != 14.95 {
		t.Errorf("FundingHedgeMinNetStore = %v, want 14.95 (D-085 含基准)", c.FundingHedgeMinNetStore)
	}
	if c.PerpFundingMinNetStore != 2.0 {
		t.Errorf("PerpFundingMinNetStore = %v, want 2.0 (D-085)", c.PerpFundingMinNetStore)
	}
	if c.CarryMinNetStore != 4.0 {
		t.Errorf("CarryMinNetStore = %v, want 4.0 (D-085)", c.CarryMinNetStore)
	}
	if c.VerifyWindowDays != 30 {
		t.Errorf("VerifyWindowDays = %v, want 30 (D-086)", c.VerifyWindowDays)
	}
	if c.VerifyMinSamples != 2 {
		t.Errorf("VerifyMinSamples = %v, want 2 (D-086)", c.VerifyMinSamples)
	}
	if c.VerifyMinRatio != 0.7 {
		t.Errorf("VerifyMinRatio = %v, want 0.7 (D-086)", c.VerifyMinRatio)
	}
	if c.MTDisasterSlPct != 2.0 {
		t.Errorf("MTDisasterSlPct = %v, want 2.0 (D-146 S1)", c.MTDisasterSlPct)
	}
}

// TestLoadEnvOverride env 覆盖须生效。
func TestLoadEnvOverride(t *testing.T) {
	t.Setenv("ARB_CROSS_MIN_GROSS_BPS_CEX", "0.5")
	t.Setenv("ARB_CROSS_MIN_GROSS_BPS_CFD", "1.5")
	t.Setenv("ARB_COMMISSION_CFD_BPS", "0.6")
	t.Setenv("ARB_DETECTOR_FRESHNESS_TTL", "90s")
	t.Setenv("ARB_LIQ_MMR", "0.01")
	t.Setenv("ARB_COMMISSION_CEX_TAKER_BPS", "3.0") // D-145：env 覆盖默认 5.0
	c := Load()
	if c.CrossVenueMinGrossBpsCEX != 0.5 {
		t.Errorf("CrossVenueMinGrossBpsCEX = %v, want 0.5", c.CrossVenueMinGrossBpsCEX)
	}
	if c.CrossVenueMinGrossBpsCFD != 1.5 {
		t.Errorf("CrossVenueMinGrossBpsCFD = %v, want 1.5", c.CrossVenueMinGrossBpsCFD)
	}
	if c.CommissionCFDBps != 0.6 {
		t.Errorf("CommissionCFDBps = %v, want 0.6", c.CommissionCFDBps)
	}
	if c.CommissionCEXTakerBps != 3.0 {
		t.Errorf("CommissionCEXTakerBps = %v, want 3.0 (D-145 env 覆盖)", c.CommissionCEXTakerBps)
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
	if c.CommissionCEXTakerBps != 5.0 {
		t.Errorf("CommissionCEXTakerBps = %v, want default 5.0 (D-145)", c.CommissionCEXTakerBps)
	}
}

// TestLoadEnvInvalid 非法值回退默认（不崩溃）。
func TestLoadEnvInvalid(t *testing.T) {
	t.Setenv("ARB_SLIPPAGE_BPS", "not-a-number")
	t.Setenv("ARB_OPPORTUNITY_TTL", "xyz")
	c := Load()
	if c.SlippageBps != 0.3 {
		t.Errorf("SlippageBps = %v, want default 0.3 (D-070)", c.SlippageBps)
	}
	if c.OpportunityTTL != 30*time.Second {
		t.Errorf("OpportunityTTL = %v, want default 30s", c.OpportunityTTL)
	}
}
