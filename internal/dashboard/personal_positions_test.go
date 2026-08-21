package dashboard

import (
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

func TestPersonalPositionToProto_MidAndLevel(t *testing.T) {
	warnHigh := 0.36
	critHigh := 0.3695
	p := &pgstore.PersonalPosition{
		ID: 1, Asset: "TRX", Kind: "short_hedge", PriceSymbol: "TRXUSDT",
		WarnHigh: &warnHigh, CritHigh: &critHigh, Note: "n", Status: "OPEN",
	}
	base := time.Unix(1000, 0)
	// 多 venue 同 canonical：取 Time 最新一帧
	venues := map[string]bus.Quote{
		"binance:spot": {Symbol: "TRXUSDT", Venue: "binance", VenueType: "spot", Bid: 0.361, Ask: 0.363, Time: base},
		"okx:perp":     {Symbol: "TRXUSDT", Venue: "okx", VenueType: "perp", Bid: 0.370, Ask: 0.372, Time: base.Add(time.Second)},
	}
	pb := personalPositionToProto(p, venues, time.Minute, base.Add(2*time.Second), 0, 0.005)
	if pb.Price != 0.371 {
		t.Errorf("price want latest frame mid 0.371, got %v", pb.Price)
	}
	if pb.Level != "crit" {
		t.Errorf("level want crit (0.371 ≥ 0.3695), got %q", pb.Level)
	}
	if pb.WarnHigh != 0.36 || pb.CritHigh != 0.3695 {
		t.Errorf("threshold passthrough want 0.36/0.3695, got %v/%v", pb.WarnHigh, pb.CritHigh)
	}
	if pb.Asset != "TRX" || pb.Kind != "short_hedge" || pb.PriceSymbol != "TRXUSDT" || pb.Status != "OPEN" {
		t.Errorf("field passthrough wrong: %+v", pb)
	}
}

func TestPersonalPositionToProto_WarnLevel(t *testing.T) {
	warnHigh := 0.36
	critHigh := 0.3695
	p := &pgstore.PersonalPosition{
		ID: 1, Asset: "TRX", Kind: "short_hedge", PriceSymbol: "TRXUSDT",
		WarnHigh: &warnHigh, CritHigh: &critHigh, Status: "OPEN",
	}
	venues := map[string]bus.Quote{
		"binance:spot": {Symbol: "TRXUSDT", Bid: 0.3605, Ask: 0.3615, Time: time.Unix(1000, 0)},
	}
	pb := personalPositionToProto(p, venues, time.Minute, time.Unix(1001, 0), 0, 0.005)
	if pb.Price != 0.361 {
		t.Errorf("price want 0.361, got %v", pb.Price)
	}
	if pb.Level != "warn" {
		t.Errorf("level want warn (0.361 ≥ 0.36 < 0.3695), got %q", pb.Level)
	}
}

// TestPersonalPositionToProto_StaleFrame（G3）：帧超陈旧 TTL → 视为无报价（price=0/level=”）；
// 混陈旧 + 新帧 → 取新帧（陈旧帧不冒充现价）。
func TestPersonalPositionToProto_StaleFrame(t *testing.T) {
	warnHigh := 0.36
	p := &pgstore.PersonalPosition{
		ID: 1, Asset: "TRX", Kind: "short_hedge", PriceSymbol: "TRXUSDT",
		WarnHigh: &warnHigh, CritHigh: nil, Status: "OPEN",
	}
	ttl := 60 * time.Second
	now := time.Unix(2000, 0)

	// 全陈旧：Time 距今 2×TTL → 无报价
	allStale := map[string]bus.Quote{
		"binance:spot": {Symbol: "TRXUSDT", Bid: 0.361, Ask: 0.361, Time: now.Add(-2 * ttl)},
	}
	pb := personalPositionToProto(p, allStale, ttl, now, 0, 0.005)
	if pb.Price != 0 || pb.Level != "" {
		t.Errorf("all-stale want price=0/level='', got %v/%q", pb.Price, pb.Level)
	}

	// 混陈旧 + 新帧：取新帧（TTL 内）
	mixed := map[string]bus.Quote{
		"binance:spot": {Symbol: "TRXUSDT", Bid: 0.361, Ask: 0.361, Time: now.Add(-2 * ttl)},
		"okx:perp":     {Symbol: "TRXUSDT", Bid: 0.365, Ask: 0.365, Time: now.Add(-time.Second)},
	}
	pb = personalPositionToProto(p, mixed, ttl, now, 0, 0.005)
	if pb.Price != 0.365 {
		t.Errorf("mixed want fresh frame mid 0.365, got %v", pb.Price)
	}
	if pb.Level != "warn" {
		t.Errorf("mixed want level warn (0.365 ≥ 0.36), got %q", pb.Level)
	}
}

func TestPersonalPositionToProto_NoQuote(t *testing.T) {
	p := &pgstore.PersonalPosition{
		ID: 2, Asset: "TRX", Kind: "staking", PriceSymbol: "TRXUSDT", Status: "OPEN",
	}
	// 无报价 → price 保持 0、level 空（诚实空态，前端渲染 "—"）
	pb := personalPositionToProto(p, nil, time.Minute, time.Now(), 0, 0.005)
	if pb.Price != 0 {
		t.Errorf("no quote price want 0, got %v", pb.Price)
	}
	if pb.Level != "" {
		t.Errorf("no quote level want empty, got %q", pb.Level)
	}
	if pb.WarnHigh != 0 || pb.CritHigh != 0 {
		t.Errorf("unset thresholds want 0/0, got %v/%v", pb.WarnHigh, pb.CritHigh)
	}
}
