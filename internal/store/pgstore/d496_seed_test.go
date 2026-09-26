// D-496：broker_symbol_map.venue→broker_accounts.venue FK 存在后，映射夹具须先落父行。
package pgstore

import (
	"context"
	"testing"
)

// seedBrokerVenue 为测试 venue 落 broker_accounts 父行（mt5 通道占位）。
func seedBrokerVenue(t *testing.T, s *Store, venues ...string) {
	t.Helper()
	for _, v := range venues {
		if _, err := s.pool.Exec(context.Background(),
			`INSERT INTO broker_accounts (venue, venue_type, host, port, password)
			 VALUES ($1,'mt5','h',443,'x') ON CONFLICT (venue) DO NOTHING`, v); err != nil {
			t.Fatalf("seed broker venue %s: %v", v, err)
		}
	}
}
