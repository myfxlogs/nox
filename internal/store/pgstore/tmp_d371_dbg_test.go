package pgstore

import (
	"context"
	"testing"
)

func TestTmpD371Dbg(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE broker_accounts, exchange_configs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertExchangeConfig(ctx, &ExchangeConfig{Venue: "binance", VenueType: "spot", Label: "demo", APIKey: "k", APISecret: "s", Simulated: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertExchangeConfig(ctx, &ExchangeConfig{Venue: "okx", VenueType: "spot", Label: "real", APIKey: "k2", APISecret: "s2", Simulated: false}); err != nil {
		t.Fatal(err)
	}
	m, err := s.ListVenueIsReal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range m {
		if v != nil {
			t.Logf("DBG map[%s]=%v", k, *v)
		} else {
			t.Logf("DBG map[%s]=nil", k)
		}
	}
}
