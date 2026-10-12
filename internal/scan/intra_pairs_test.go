// intra_pairs_test.go —— venue 动态派生（业主裁定 2026-10-01）：
// 笛卡尔积形态 / venue 集随增随删 / 查询失败零 pair + note（纸面 fail-closed）。
package scan

import (
	"context"
	"errors"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// TestBuildIntraPairs 形态断言：mt=[A,B] cex=[okx] → 1×2 CEX 对 + 2×1 MT 对，
// Crypto 标志正确（CEX=true 7×24 例外；MT=false 走时段闸）。
func TestBuildIntraPairs(t *testing.T) {
	pairs := buildIntraPairs([]string{"A", "B"}, []string{"okx"})
	if len(pairs) != 4 {
		t.Fatalf("pairs=%d, want 4（1 cex×2 sym + 2 mt×1 sym）", len(pairs))
	}
	want := map[string]bool{ // venue|symbol|crypto
		"okx|BTCUSDT|true": true, "okx|ETHUSDT|true": true,
		"A|XAUUSD|false": true, "B|XAUUSD|false": true,
	}
	seen := map[string]bool{}
	for _, p := range pairs {
		key := p.Venue + "|" + p.Symbol
		if p.Crypto {
			key += "|true"
		} else {
			key += "|false"
		}
		seen[key] = true
	}
	for k := range want {
		if !seen[k] {
			t.Errorf("缺 pair %s；got %v", k, seen)
		}
	}
}

// TestIntraPairs_DynamicVenue venue 集变化 → 下一 Step 立即反映（随增随删）。
// 构造信号充足的 ticks（单调上行动量破位），mt venue 先行从 [X] 变 [X,Y]：
// Concurrency 放宽到 2 让两对都能开。
func TestIntraPairs_DynamicVenue(t *testing.T) {
	now := time.Now().UTC()
	f := &fakeIntraStore{
		mtVenues:  []string{"X1"},
		cexVenues: []string{},
		ticks: map[string][]pgstore.TickRow{
			"X1|XAUUSD": monotonic(2000, 2, 90, now.Add(-90*time.Minute)),
			"X2|XAUUSD": monotonic(2000, 2, 90, now.Add(-90*time.Minute)),
		},
	}
	cfg := d425Cfg(false)
	cfg.Concurrency = 2
	cfg.SessionGate = false // 排除时段闸干扰（断言 venue 派生而非时段）
	eng := NewIntraEngine(f, cfg)
	eng.SetNow(func() time.Time { return now })

	rep, err := eng.Step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Opened == 0 {
		t.Fatal("mt=[X1] 应至少开 1 仓（动量破位信号充足）")
	}
	for _, tr := range f.inserts {
		if tr.Venue != "X1" {
			t.Errorf("首步开仓 venue=%s, want X1（X2 尚未配置）", tr.Venue)
		}
	}
	// venue 增 X2 → 下一 Step 新仓可落在 X2（X1 同对已开不重复）。
	f.mtVenues = []string{"X1", "X2"}
	rep, err = eng.Step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var x2 bool
	for _, tr := range f.inserts {
		if tr.Venue == "X2" {
			x2 = true
		}
	}
	if !x2 {
		t.Errorf("mt 增至 [X1,X2] 后应能开 X2 仓；inserts=%v", f.inserts)
	}
}

// TestIntraPairs_VenueErrFailClosed ListIntraVenues 失败 → 零 pair + note，不开新仓。
func TestIntraPairs_VenueErrFailClosed(t *testing.T) {
	now := time.Now().UTC()
	f := &fakeIntraStore{
		venuesErr: errors.New("db down"),
		ticks: map[string][]pgstore.TickRow{
			"binance|BTCUSDT": monotonic(100, 1, 90, now.Add(-90*time.Minute)),
		},
	}
	eng := NewIntraEngine(f, d425Cfg(false))
	eng.SetNow(func() time.Time { return now })
	rep, err := eng.Step(context.Background())
	if err != nil {
		t.Fatalf("venue 查询失败应吞进 note 不上抛（纸面轨），got %v", err)
	}
	if len(f.inserts) != 0 {
		t.Errorf("venue 查询失败本轮应零开仓，got %d", len(f.inserts))
	}
	var noted bool
	for _, n := range rep.Notes {
		if n == "intra venues: db down（本轮不开新仓）" {
			noted = true
		}
	}
	if !noted {
		t.Errorf("缺 venues 失败 note，got %v", rep.Notes)
	}
}
