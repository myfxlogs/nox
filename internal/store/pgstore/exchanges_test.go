package pgstore

import (
	"context"
	"testing"
)

// TestExchangeConfigsCRUD 交易所配置增删查 + UNIQUE 冲突（D-054）。
func TestExchangeConfigsCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE exchange_configs RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	c := &ExchangeConfig{
		Venue: "okx", VenueType: "spot", Label: "demo",
		APIKey: "k-okx", APISecret: "s-okx", Passphrase: "p",
		BaseURL: "", Simulated: true, // OKX demo：默认域名 + x-simulated-trading:1
	}
	if err := s.InsertExchangeConfig(ctx, c); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if c.ID <= 0 {
		t.Fatalf("id 未回填：%d", c.ID)
	}
	if c.CreatedAt.IsZero() {
		t.Fatalf("created_at 未回填")
	}

	// 同 venue+venue_type+simulated 冲突（D-271 R1：唯一约束含 simulated 列）
	dup := &ExchangeConfig{Venue: "okx", VenueType: "spot", APIKey: "k2", APISecret: "s2", Simulated: true}
	if err := s.InsertExchangeConfig(ctx, dup); !ErrExchangeConflict(err) {
		t.Fatalf("期望 UNIQUE 冲突，got %v", err)
	}

	// 同 venue+venue_type 但 simulated 不同 → 不冲突（D-271 R1：demo+main 共存）
	main := &ExchangeConfig{Venue: "okx", VenueType: "spot", APIKey: "k-main", APISecret: "s-main", Simulated: false}
	if err := s.InsertExchangeConfig(ctx, main); err != nil {
		t.Fatalf("insert main (simulated=false): %v", err)
	}

	// 不同 venue_type 不冲突
	perp := &ExchangeConfig{Venue: "okx", VenueType: "perp", APIKey: "k3", APISecret: "s3"}
	if err := s.InsertExchangeConfig(ctx, perp); err != nil {
		t.Fatalf("insert perp: %v", err)
	}

	list, err := s.ListExchangeConfigs(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("期望 3 条（demo spot + main spot + perp），got %d", len(list))
	}
	if list[0].Venue != "okx" || list[0].APIKey != "k-okx" || list[0].Passphrase != "p" {
		t.Fatalf("字段往返不一致：%+v", list[0])
	}
	if !list[0].Simulated {
		t.Fatalf("simulated 未往返：%+v", list[0])
	}

	if err := s.DeleteExchangeConfig(ctx, c.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list2, _ := s.ListExchangeConfigs(ctx)
	if len(list2) != 2 {
		t.Fatalf("删除后期望 2 条（main spot + perp），got %d", len(list2))
	}
}
