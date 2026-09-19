package scan

// D-336 T1-T4：宇宙扫描器——join/排序/结构选优/告警阈值。
import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// fakeScanStore 受控 store（facts 行+映射表+落库计数）。
type fakeScanStore struct {
	facts    []fact.Fact
	mappings []pgstore.SymbolMapping
	replaced []pgstore.CarryUniverseRow
	alerts   []string
}

func (f *fakeScanStore) LatestFacts(_ context.Context, kind, _, _ string) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, f := range f.facts {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out, nil
}
func (f *fakeScanStore) ListSymbolMappings(_ context.Context, venue string) ([]pgstore.SymbolMapping, error) {
	var out []pgstore.SymbolMapping
	for _, m := range f.mappings {
		if m.Venue == venue {
			out = append(out, m)
		}
	}
	return out, nil
}
func (f *fakeScanStore) ReplaceCarryUniverse(_ context.Context, rows []pgstore.CarryUniverseRow) error {
	f.replaced = rows
	return nil
}

// TestD336_ScanJoinAndRank T1/T2：BTC（swap 0/0 swap_free）+负 carry canonical——
// BTC 行 net≈6.65 swap_free=true 居首；负行殿后。
func TestD336_ScanJoinAndRank(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "BTCUSDT", Value: 6.65, Ts: time.Now()},
			{Kind: "swap_long", Venue: "277842155", Symbol: "BTCUSD", Value: 0, Ts: time.Now()},
			{Kind: "swap_short", Venue: "277842155", Symbol: "BTCUSD", Value: 0, Ts: time.Now()},
			{Kind: "funding", Venue: "binance", Symbol: "ETHUSDT", Value: 2.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "277842155", Symbol: "ETHUSD", Value: -30, Ts: time.Now()}, // net=2−(−30)=32？语义：多 CFD 成本 swap_long（负=付）→ net=funding−swap_long=2−(−30)=32 正
			{Kind: "funding", Venue: "okx", Symbol: "WTIUSDT", Value: 1.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "277842155", Symbol: "OILUSD", Value: -5, Ts: time.Now()}, // net=1−(−5)=6
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "277842155", BrokerSymbol: "BTCUSD", Canonical: "BTC"},
			{Venue: "277842155", BrokerSymbol: "ETHUSD", Canonical: "ETH"},
			{Venue: "277842155", BrokerSymbol: "OILUSD", Canonical: "WTI"},
		},
	}
	sc := NewCarryUniverseScanner(st)
	res, err := sc.Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("rows=%d, want 3（红=现无此机制）: %+v", len(res.Rows), res.Rows)
	}
	// 排序：ETH 32 > BTC 6.65 > WTI 6
	if res.Rows[0].Canonical != "ETH" || res.Rows[1].Canonical != "BTC" || res.Rows[2].Canonical != "WTI" {
		t.Errorf("排序错: %v", res.Rows)
	}
	// T1：BTC swap_free（swap 0/0）net≈funding
	if !res.Rows[1].SwapFree {
		t.Error("BTC swap_long=0 应标记 swap_free")
	}
	if res.Rows[1].NetAnnual < 6.6 || res.Rows[1].NetAnnual > 6.7 {
		t.Errorf("BTC net want ≈6.65, got %v", res.Rows[1].NetAnnual)
	}
	// 落库
	if err := st.ReplaceCarryUniverse(context.Background(), toPgRows(res.Rows)); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(st.replaced) != 3 {
		t.Errorf("落库行数=%d, want 3", len(st.replaced))
	}
}

// TestD336_StructurePick T4：同 canonical 双 venue swap（−8.22 vs 0/0）→ 最优选 0/0（swap_free）。
func TestD336_StructurePick(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "XAUUSDT", Value: 5.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "591959217", Symbol: "XAUUSD", Value: -8.22, Ts: time.Now()},
			{Kind: "swap_long", Venue: "277842155", Symbol: "XAUUSD", Value: 0, Ts: time.Now()},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "591959217", BrokerSymbol: "XAUUSD", Canonical: "XAU"},
			{Venue: "277842155", BrokerSymbol: "XAUUSD", Canonical: "XAU"},
		},
	}
	sc := NewCarryUniverseScanner(st)
	res, err := sc.Run(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows=%d skipped=%d, want 1: %+v", len(res.Rows), res.Skipped, res.Rows)
	}
	r := res.Rows[0]
	// 多 CFD 成本：0 优于 −8.22 → net=5−0=5（swap_free）
	if r.MtVenue != "277842155" || !r.SwapFree {
		t.Errorf("最优 venue 选 0/0（swap_free）, got %+v", r)
	}
}

// TestD336_CanonicalJoin T3：CEX 无 USDT 后缀/MT 无映射 → skip 计数。
func TestD336_CanonicalJoin(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "BTCUSD", Value: 5.0, Ts: time.Now()}, // 无 USDT 后缀
			{Kind: "swap_long", Venue: "ghost", Symbol: "XXXUSD", Value: -5, Ts: time.Now()},  // 无映射
		},
		mappings: []pgstore.SymbolMapping{},
	}
	sc := NewCarryUniverseScanner(st)
	res, err := sc.Run(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Rows) != 0 || res.Skipped != 2 {
		t.Errorf("无法映射应全 skip: rows=%d skipped=%d, want 0/2", len(res.Rows), res.Skipped)
	}
}

// TestD336_ThresholdAlert T4 告警锚：net≥8 且 swap_free → alert；4-8% 观察线不推。
func TestD336_ThresholdAlert(t *testing.T) {
	rows := []UniverseRow{
		{Canonical: "HOT", NetAnnual: 9.0, SwapFree: true, Structure: "long_cfd_short_perp", MtVenue: "a", CexVenue: "b"},
		{Canonical: "WARM", NetAnnual: 6.65, SwapFree: true, Structure: "long_cfd_short_perp", MtVenue: "a", CexVenue: "b"},
		{Canonical: "NOTFREE", NetAnnual: 12.0, SwapFree: false, Structure: "long_cfd_short_perp", MtVenue: "a", CexVenue: "b"},
	}
	var got []string
	EmitThresholdAlerts(context.Background(), func(_ context.Context, _, _ string) error {
		return nil
	}, rows, func(_ context.Context, ruleID *int64, level, message string) error {
		got = append(got, level+"|"+message)
		return nil
	})
	if len(got) != 1 || !containsSub(got[0], "HOT") || !containsSub(got[0], "真候选浮现") {
		t.Errorf("仅 HOT（8%% 且 swap_free）应告警, got %v", got)
	}
}

func containsSub(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func toPgRows(rows []UniverseRow) []pgstore.CarryUniverseRow {
	out := make([]pgstore.CarryUniverseRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, pgstore.CarryUniverseRow{
			Canonical: r.Canonical, MtVenue: r.MtVenue, CexVenue: r.CexVenue,
			NetAnnualPct: r.NetAnnual, FundingAnnualPct: r.FundingAnn,
			SwapLongPct: r.SwapLongPct, SwapShortPct: r.SwapShortPct,
			SwapFree: r.SwapFree, Structure: r.Structure,
		})
	}
	return out
}
