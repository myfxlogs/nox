package dciexec

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestConcentration_LiveMapDenominator 浓度透视分母改实拉锚：fake map 三 venue
// →分母=实拉值（binance 9000/10500=85.7%≥50%→浓度行），快照零消费。
func TestConcentration_LiveMapDenominator(t *testing.T) {
	ex, f, _ := earnFixture()
	ex.LiveEquityMapFn = func(context.Context) (map[string]float64, error) {
		return map[string]float64{
			"binance:spot:USDT": 9000, "okx:unified:USDT": 500, "m:mt5:USD": 1000,
		}, nil
	}
	ex.liveConcentration(context.Background())
	var saw bool
	for _, a := range f.alerts {
		if strings.Contains(a, "浓度透视") && strings.Contains(a, "binance=85.7%") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("浓度行（实拉分母）缺: %v", f.alerts)
	}
}

// TestConcentration_LiveMapFailWarn 拉败→warn 剔分母（fail-closed 语义保留）。
func TestConcentration_LiveMapFailWarn(t *testing.T) {
	ex, f, _ := earnFixture()
	ex.LiveEquityMapFn = func(context.Context) (map[string]float64, error) {
		return nil, errors.New("binance 拉败")
	}
	ex.liveConcentration(context.Background())
	var saw bool
	for _, a := range f.alerts {
		if strings.Contains(a, "浓度透视实拉败") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("实拉败 warn 缺: %v", f.alerts)
	}
}

// TestConcentration_NoStaleText 「分母池鲜度失」伪信号族根治——实拉径零产该文案。
func TestConcentration_NoStaleText(t *testing.T) {
	ex, f, _ := earnFixture()
	ex.LiveEquityMapFn = func(context.Context) (map[string]float64, error) {
		return map[string]float64{"binance:spot:USDT": 9000, "m:mt5:USD": 1000}, nil
	}
	ex.liveConcentration(context.Background())
	for _, a := range f.alerts {
		if strings.Contains(a, "分母池鲜度失") {
			t.Fatalf("鲜度失文案应已删: %q", a)
		}
	}
}
