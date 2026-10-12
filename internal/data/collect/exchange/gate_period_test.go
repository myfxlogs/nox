package exchange

// 位点③测试：Gate funding 结算周期数据驱动推导（相邻 t 差众数→每日次数）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestD334ab_GatePeriodDerive T4：众数推导——4h 序列→6（升序/降序）、8h→3、
// 4h 掺 28800 缺口→众数仍 6、样本行<3→回退 3、众数不唯一→回退 3。
func TestD334ab_GatePeriodDerive(t *testing.T) {
	const base = float64(1788163200)
	feed := func(ts ...float64) *gatePeriodDeriver {
		d := newGatePeriodDeriver()
		for _, t := range ts {
			d.observe(t)
		}
		return d
	}
	if got := feed(base, base+14400, base+28800).periodsPerDay(); got != 6 {
		t.Errorf("asc 4h sequence want 6, got %d", got)
	}
	if got := feed(base+28800, base+14400, base).periodsPerDay(); got != 6 {
		t.Errorf("desc 4h sequence want 6, got %d", got)
	}
	if got := feed(base, base+28800, base+57600).periodsPerDay(); got != 3 {
		t.Errorf("8h sequence want 3, got %d", got)
	}
	if got := feed(base, base+14400, base+43200, base+57600).periodsPerDay(); got != 6 {
		t.Errorf("4h with one 28800 gap want mode 6, got %d", got)
	}
	if got := feed(base, base+14400).periodsPerDay(); got != 3 {
		t.Errorf("<3 rows want fallback 3, got %d", got)
	}
	if got := feed(base, base+14400, base+43200).periodsPerDay(); got != 3 {
		t.Errorf("no unique mode (14400/28800 1:1 tie) want fallback 3, got %d", got)
	}
}

// TestD334ab_GatePoll4hDerived T4 集成锚：Gate 4h 序列（3 行降序）→ 年化=rate×6×365×100
// =21.9（红=现码恒 ×3 产 10.95）。
func TestD334ab_GatePoll4hDerived(t *testing.T) {
	const base = float64(1788163200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"r": "0.0001", "t": base + 28800},
			{"r": "0.0001", "t": base + 14400},
			{"r": "0.0001", "t": base},
		})
	}))
	defer srv.Close()

	cfg := Config{Symbols: []string{"XAU"}, GateBaseURL: srv.URL, Client: srv.Client()}
	out, err := NewGateFundingHistory(cfg, zeroResolver).Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3 facts, got %d", len(out))
	}
	for _, f := range out {
		if f.Value < 21.8 || f.Value > 22.0 {
			t.Errorf("4h derived want ~21.9 (rate×6), got %.4f（红=现码恒 ×3）", f.Value)
		}
		if f.Unit != "pct_annualized" {
			t.Errorf("unit want pct_annualized, got %s", f.Unit)
		}
	}
	// ts 不受推导影响（Gate 秒）
	if !out[0].Ts.Equal(time.Unix(int64(base+28800), 0)) {
		t.Errorf("ts corrupted: %v", out[0].Ts)
	}
}
