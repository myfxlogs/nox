package swapledger

import (
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

func TestReconcileLeg_FormulaEqualsActual(t *testing.T) {
	// 公式值=实际增量 → ok=true, deviationPct=0 → 不告警
	today := pgstore.SwapSnapshot{ID: 2, SwapUSD: 10.0, Ts: time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)}
	yesterday := pgstore.SwapSnapshot{ID: 1, SwapUSD: 5.0, Ts: time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)}
	// actualUSD = 10 - 5 = 5; formulaUSD = 5 → deviation=0
	dev, ok := reconcileLeg(5.0, today, yesterday)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if dev != 0 {
		t.Fatalf("expected deviationPct=0, got %.4f", dev)
	}
	if level := alertLevel(dev, 10.0); level != "" {
		t.Fatalf("expected no alert, got %s", level)
	}
}

func TestReconcileLeg_DeviationExceedsThreshold(t *testing.T) {
	// 公式值=11, 实际增量=5 → deviation=6, denom=11 → 54.5% → critical
	today := pgstore.SwapSnapshot{ID: 2, SwapUSD: 10.0, Ts: time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)}
	yesterday := pgstore.SwapSnapshot{ID: 1, SwapUSD: 5.0, Ts: time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)}
	// actualUSD = 5; formulaUSD = 11 → deviation=6, denom=max(11,5,0.01)=11 → 54.5%
	dev, ok := reconcileLeg(11.0, today, yesterday)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if dev <= 50.0 {
		t.Fatalf("expected deviationPct>50, got %.4f", dev)
	}
	if level := alertLevel(dev, 10.0); level != "critical" {
		t.Fatalf("expected critical, got %s", level)
	}
}

func TestReconcileLeg_NoSnapshot(t *testing.T) {
	// todaySnap 零值 → ok=false
	_, ok := reconcileLeg(5.0, pgstore.SwapSnapshot{}, pgstore.SwapSnapshot{ID: 1, SwapUSD: 5.0})
	if ok {
		t.Fatal("expected ok=false for zero todaySnap")
	}
	// yesterdaySnap 零值 → ok=false
	_, ok = reconcileLeg(5.0, pgstore.SwapSnapshot{ID: 2, SwapUSD: 10.0}, pgstore.SwapSnapshot{})
	if ok {
		t.Fatal("expected ok=false for zero yesterdaySnap")
	}
}

func TestReconcileLeg_TodayNotSettled(t *testing.T) {
	// todaySnap.Ts <= yesterdaySnap.Ts → ok=false（D1）
	yesterdayTs := time.Date(2026, 8, 26, 2, 0, 0, 0, time.UTC)
	todayTs := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC) // earlier than yesterday
	today := pgstore.SwapSnapshot{ID: 2, SwapUSD: 10.0, Ts: todayTs}
	yesterday := pgstore.SwapSnapshot{ID: 1, SwapUSD: 5.0, Ts: yesterdayTs}
	_, ok := reconcileLeg(5.0, today, yesterday)
	if ok {
		t.Fatal("expected ok=false when todaySnap.Ts <= yesterdaySnap.Ts")
	}
}

func TestReconcileLeg_ExnessScenario(t *testing.T) {
	// Exness: actual=0 (swap 恒=0), formula>0 → deviationPct=100 → critical
	today := pgstore.SwapSnapshot{ID: 2, SwapUSD: 0.0, Ts: time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)}
	yesterday := pgstore.SwapSnapshot{ID: 1, SwapUSD: 0.0, Ts: time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)}
	// actualUSD = 0; formulaUSD = 4.84 → deviation=4.84, denom=max(4.84,0,0.01)=4.84 → 100%
	dev, ok := reconcileLeg(4.84, today, yesterday)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if dev != 100.0 {
		t.Fatalf("expected deviationPct=100, got %.4f", dev)
	}
	if level := alertLevel(dev, 10.0); level != "critical" {
		t.Fatalf("expected critical, got %s", level)
	}
}

func TestAlertLevel_ThresholdBoundary(t *testing.T) {
	// deviationPct == 阈值不告警（严格 >）
	if level := alertLevel(10.0, 10.0); level != "" {
		t.Fatalf("expected no alert at threshold, got %s", level)
	}
	// deviationPct = 10.01 > 阈值 → warn
	if level := alertLevel(10.01, 10.0); level != "warn" {
		t.Fatalf("expected warn at 10.01, got %s", level)
	}
}

func TestAlertLevel_Grading(t *testing.T) {
	// <10% → ""
	if level := alertLevel(5.0, 10.0); level != "" {
		t.Fatalf("expected no alert at 5%%, got %s", level)
	}
	// 10-50% → warn
	if level := alertLevel(30.0, 10.0); level != "warn" {
		t.Fatalf("expected warn at 30%%, got %s", level)
	}
	// >50% → critical
	if level := alertLevel(60.0, 10.0); level != "critical" {
		t.Fatalf("expected critical at 60%%, got %s", level)
	}
	// ==50% → warn（不 >50）
	if level := alertLevel(50.0, 10.0); level != "warn" {
		t.Fatalf("expected warn at exactly 50%%, got %s", level)
	}
}
