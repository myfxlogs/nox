package dashboard

// D-560 T4 透出锚：近6h FAILED 资金拒批量透出（禁 N+1 一次查）→ proto 行
// recent_funding_reject/recent_reject_reason 落成且尾部含「解法：」；非资金拒不标。
import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/errclass"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

func TestD560_ComboRowFundingReject(t *testing.T) {
	f, s := gradFixture("long_cfd_short_perp")
	n := 1750.0
	f.adminRows = []pgstore.XAssetComboAdminRow{{
		ID: 12, CanonicalBuy: "XAUUSDT", CanonicalSell: "XAUUSD",
		VenueBuy: "gate:perp", VenueSell: "3001157:mt5",
		MaxLongQty: 500, MaxShortQty: 1, MaxGrossUSD: 5000, NotionalUSD: &n, Enabled: true,
	}}
	f.failedLogs = []pgstore.AutotradeLog{
		{Canonical: "XAUUSDT", VenueBuy: "gate:perp", VenueSell: "3001157:mt5",
			Status: "FAILED", Ts: time.Now(),
			Reason: "gate margin insufficient: need 830.00 > avail 1000.00 × 0.80 (lev 5 mark 4350.12)"},
		{Canonical: "XAUUSDT", VenueBuy: "gate:perp", VenueSell: "3001157:mt5",
			Status: "FAILED", Ts: time.Now().Add(-time.Hour),
			Reason: "MARKET_CLOSED"}, // 非资金拒不污染（且较旧）
	}
	resp, err := s.ListXAssetAllowCombos(context.Background(), connect.NewRequest(&v1.ListXAssetAllowCombosRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.Msg.Rows) != 1 {
		t.Fatalf("rows=%d", len(resp.Msg.Rows))
	}
	r := resp.Msg.Rows[0]
	if !r.RecentFundingReject {
		t.Fatalf("recent_funding_reject 应真: %+v", r)
	}
	if !strings.Contains(r.RecentRejectReason, "解法：") || !strings.Contains(r.RecentRejectReason, "≥$50") {
		t.Fatalf("reason 尾部应含解法行（补$50）, got %q", r.RecentRejectReason)
	}
}

// 无资金拒行 → 字段零值（不误标）。
func TestD560_ComboRowNoReject(t *testing.T) {
	f, s := gradFixture("long_cfd_short_perp")
	n := 1750.0
	f.adminRows = []pgstore.XAssetComboAdminRow{{
		ID: 12, CanonicalBuy: "XAUUSDT", CanonicalSell: "XAUUSD",
		VenueBuy: "gate:perp", VenueSell: "3001157:mt5",
		MaxLongQty: 500, MaxShortQty: 1, MaxGrossUSD: 5000, NotionalUSD: &n, Enabled: true,
	}}
	f.failedLogs = []pgstore.AutotradeLog{{
		Canonical: "XAUUSDT", VenueBuy: "gate:perp", VenueSell: "3001157:mt5",
		Status: "FAILED", Reason: "quote venue symbol XAUUSD247m != primary XAUUSDm", Ts: time.Now(),
	}}
	resp, err := s.ListXAssetAllowCombos(context.Background(), connect.NewRequest(&v1.ListXAssetAllowCombosRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if r := resp.Msg.Rows[0]; r.RecentFundingReject || r.RecentRejectReason != "" {
		t.Fatalf("variant 拒文非资金拒不应透出, got %+v", r)
	}
}

// ===== D-560 S3 T7：行动线锚——breaker_* 三字段透出 + ResumeXAssetCombo 清态 =====

type fakeBreakerReader struct {
	snap    map[string]errclass.RejectBreakerInfo
	resumed []string
}

func (f *fakeBreakerReader) BreakerSnapshot() map[string]errclass.RejectBreakerInfo { return f.snap }
func (f *fakeBreakerReader) ResumeBreaker(dedupKey string) int {
	f.resumed = append(f.resumed, dedupKey)
	n := 0
	for k := range f.snap {
		if strings.HasPrefix(k, dedupKey+"|") {
			n++
		}
	}
	return n
}

func TestD560_BreakerFieldsAndResume(t *testing.T) {
	f, s := gradFixture("long_cfd_short_perp")
	n := 1750.0
	f.adminRows = []pgstore.XAssetComboAdminRow{{
		ID: 12, CanonicalBuy: "XAUUSDT", CanonicalSell: "XAUUSD",
		VenueBuy: "gate:perp", VenueSell: "3001157:mt5",
		MaxLongQty: 500, MaxShortQty: 1, MaxGrossUSD: 5000, NotionalUSD: &n, Enabled: true,
	}}
	dk := "XASSET_CARRY:XAUUSDT:gate:perp:3001157:mt5"
	fb := &fakeBreakerReader{snap: map[string]errclass.RejectBreakerInfo{
		dk + "|funding": {Class: "funding", Count: 5, Paused: true, ResumeAt: time.Now().Add(2 * time.Hour)},
	}}
	s.SetRejectBreaker(fb)

	resp, err := s.ListXAssetAllowCombos(context.Background(), connect.NewRequest(&v1.ListXAssetAllowCombosRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	r := resp.Msg.Rows[0]
	if !r.BreakerPaused || r.BreakerClass != "funding" || r.BreakerResumeAt == nil || r.BreakerCount != 5 {
		t.Fatalf("breaker_* 三字段未透出: %+v", r)
	}

	rr, err := s.ResumeXAssetCombo(context.Background(), connect.NewRequest(&v1.ResumeXAssetComboRequest{ComboId: 12}))
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !rr.Msg.Ok || rr.Msg.Cleared != 1 {
		t.Fatalf("resume 应清 1 条, got %+v", rr.Msg)
	}
	if len(fb.resumed) == 0 || fb.resumed[0] != dk {
		t.Fatalf("ResumeBreaker 应命中组合 dedupKey, got %v", fb.resumed)
	}
	// 不存在组合 → ok=false
	bad, _ := s.ResumeXAssetCombo(context.Background(), connect.NewRequest(&v1.ResumeXAssetComboRequest{ComboId: 999}))
	if bad.Msg.Ok {
		t.Fatalf("未知 combo 不应 ok")
	}
}
