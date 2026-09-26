package pgstore

import (
	"context"
	"testing"
)

// TestListVenueIsReal D-178/D-371：统一 venue:type→*bool 映射——CFD 透传（venue:platform
// 拼键）+ CEX 取反 + 两表合并。
func TestListVenueIsReal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE broker_accounts, exchange_configs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// CFD venue：is_real=true
	if _, err := s.pool.Exec(ctx, `INSERT INTO broker_accounts (venue, venue_type, host, port, password, is_real) VALUES ('1314615', 'mt5', 'h', 443, 'p', true)`); err != nil {
		t.Fatalf("insert cfd real: %v", err)
	}
	// CFD venue：is_real=false（demo）
	if _, err := s.pool.Exec(ctx, `INSERT INTO broker_accounts (venue, venue_type, host, port, password, is_real) VALUES ('5208379', 'mt4', 'h', 443, 'p', false)`); err != nil {
		t.Fatalf("insert cfd demo: %v", err)
	}
	// CFD venue：is_real=NULL（未知）
	if _, err := s.pool.Exec(ctx, `INSERT INTO broker_accounts (venue, venue_type, host, port, password, is_real) VALUES ('9999999', 'mt5', 'h', 443, 'p', NULL)`); err != nil {
		t.Fatalf("insert cfd null: %v", err)
	}
	// CEX venue：simulated=true → is_real=false
	if err := s.InsertExchangeConfig(ctx, &ExchangeConfig{
		Venue: "binance", VenueType: "spot", Label: "demo",
		APIKey: "k", APISecret: "s", Simulated: true,
	}); err != nil {
		t.Fatalf("insert cex demo: %v", err)
	}
	// CEX venue：simulated=false → is_real=true
	if err := s.InsertExchangeConfig(ctx, &ExchangeConfig{
		Venue: "okx", VenueType: "spot", Label: "real",
		APIKey: "k2", APISecret: "s2", Simulated: false,
	}); err != nil {
		t.Fatalf("insert cex real: %v", err)
	}

	m, err := s.ListVenueIsReal(ctx)
	if err != nil {
		t.Fatalf("ListVenueIsReal: %v", err)
	}

	// CFD true（key=venue:mt5）
	if v := m["1314615:mt5"]; v == nil || !*v {
		t.Fatalf("1314615:mt5 want *true, got %v", v)
	}
	// CFD false（key=venue:mt4）
	if v := m["5208379:mt4"]; v == nil || *v {
		t.Fatalf("5208379:mt4 want *false, got %v", v)
	}
	// CFD NULL → nil
	if v := m["9999999:mt5"]; v != nil {
		t.Fatalf("9999999:mt5 want nil, got %v", v)
	}
	// CEX simulated=true → is_real=false（key=venue:venue_type）
	// CEX simulated=true → is_real=false（key=venue:venue_type）
	if v := m["binance:spot"]; v == nil || *v {
		t.Fatalf("binance:spot want *false (simulated=true), got %v", v)
	}
	// CEX simulated=false → is_real=true
	if v := m["okx:spot"]; v == nil || !*v {
		t.Fatalf("okx:spot want *true (simulated=false), got %v; full map: %+v", v, m)
	}
	// 未知 venue 不在 map
	if _, ok := m["unknown"]; ok {
		t.Fatalf("unknown venue should not be in map")
	}
}

// TestBrokerRealAccountKeys D-360 S5 锚：real/demo/NULL 混合 → 仅 is_real=true 键
// （user_id::text 为键，核证点②；NULL is_real 不收=证真才留）。
func TestBrokerRealAccountKeys(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE broker_accounts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	for _, r := range []struct {
		venue  string
		userID int64
		real   *bool
	}{
		{"3001157", 3001157, boolPtr(true)},
		{"52993526", 52993526, boolPtr(false)},
		{"9999999", 9999999, nil},
	} {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO broker_accounts (venue, venue_type, host, port, password, is_real)
			 VALUES ($1, 'mt5', 'h', 443, 'p', $2)`, r.venue, r.real); err != nil {
			t.Fatalf("insert %s: %v", r.venue, err)
		}
	}
	keys, err := s.BrokerRealAccountKeys(ctx)
	if err != nil {
		t.Fatalf("BrokerRealAccountKeys: %v", err)
	}
	if !keys["3001157"] {
		t.Errorf("real 键 3001157 应在集合, got %v", keys)
	}
	if len(keys) != 1 {
		t.Errorf("键集应仅 real 1 个, got %d: %v", len(keys), keys)
	}
}

// boolPtr 测试辅助（is_real 三态 seed 用）。
func boolPtr(b bool) *bool { return &b }
