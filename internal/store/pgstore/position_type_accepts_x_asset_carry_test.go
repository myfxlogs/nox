package pgstore

// 锚①：迁移 0102 验收——positions_type_check 接受 XASSET_CARRY（六值→七值）
// + fin_cex_universe.ct_val 写入/读取四档（值命中/NULL/缺行/非正值→ok=false 不猜）。

import (
	"context"
	"testing"
	"time"
)

// TestD311_PositionTypeAcceptsXAssetCarry S8锚①a：XASSET_CARRY 过 positions_type_check。
// 台账断言 schema_migrations version=102（防 migration 未登记假绿——经验库同款坑）。
func TestD311_PositionTypeAcceptsXAssetCarry(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var applied bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = 102)`).Scan(&applied); err != nil {
		t.Fatalf("schema_migrations query: %v", err)
	}
	if !applied {
		t.Fatal("migration 0102 未应用（schema_migrations version=102 缺行）")
	}
	pos := &Position{
		OpportunityID: "XASSET_CARRY:WTIUSD:EXR3001157:mt5:gate:perp",
		Type:          "XASSET_CARRY",
		Status:        "OPEN",
	}
	if _, err := s.CreatePosition(ctx, pos); err != nil {
		t.Fatalf("XASSET_CARRY 被 positions_type_check 拒绝: %v", err)
	}
	// 回归：既有 type 仍被接受（约束重建未丢旧值）。
	pos2 := &Position{OpportunityID: "SWAPARB:XAUUSD:A:mt5:B:mt5", Type: "SWAPARB", Status: "OPEN"}
	if _, err := s.CreatePosition(ctx, pos2); err != nil {
		t.Fatalf("SWAPARB 回归被拒: %v", err)
	}
}

// TestD311_CexContractSize S8锚①b：ct_val upsert/读取四档。
// gate CL_USDT=0.01 / binance BTCUSDT=1.0 写入命中；NULL/缺行/≤0 → ok=false（fail-closed，不猜）。
func TestD311_CexContractSize(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE fin_cex_universe CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	ct001, ct1, ct0 := 0.01, 1.0, 0.0
	rows := []CexUniverseRow{
		{Venue: "gate", Symbol: "CL_USDT", Base: "CL", CtVal: &ct001},
		{Venue: "binance", Symbol: "BTCUSDT", Base: "BTC", CtVal: &ct1},
		{Venue: "okx", Symbol: "XPT-USDT-SWAP", Base: "XPT", CtVal: nil}, // NULL=未知
		{Venue: "gate", Symbol: "ZERO_USDT", Base: "ZERO", CtVal: &ct0},  // 非正值
	}
	if err := s.UpsertCexUniverse(ctx, rows, now); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// 值命中
	for _, tc := range []struct {
		venue, symbol string
		want          float64
	}{
		{"gate", "CL_USDT", 0.01},
		{"binance", "BTCUSDT", 1.0},
	} {
		got, ok, err := s.GetCexContractSize(ctx, tc.venue, tc.symbol)
		if err != nil {
			t.Fatalf("GetCexContractSize %s/%s err: %v", tc.venue, tc.symbol, err)
		}
		if !ok || got != tc.want {
			t.Errorf("%s/%s: want (%.4f,true), got (%.4f,%v)", tc.venue, tc.symbol, tc.want, got, ok)
		}
	}
	// NULL/缺行/非正值 → (0,false,nil)——不报错也不猜值（解析器据此拒单）
	for _, tc := range []struct{ venue, symbol string }{
		{"okx", "XPT-USDT-SWAP"}, // NULL
		{"gate", "ZERO_USDT"},    // ≤0
		{"gate", "NOPE_USDT"},    // 缺行
	} {
		got, ok, err := s.GetCexContractSize(ctx, tc.venue, tc.symbol)
		if err != nil {
			t.Errorf("%s/%s: want nil error, got %v", tc.venue, tc.symbol, err)
		}
		if ok || got != 0 {
			t.Errorf("%s/%s: want (0,false), got (%.4f,%v)", tc.venue, tc.symbol, got, ok)
		}
	}
}
