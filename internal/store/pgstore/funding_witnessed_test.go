// funding_witnessed_test.go —— D-280 方案 A 锚：ListFundingWitnessedSymbols
// 分所+窗过滤（funding 事实见证符号=深度采集动态基名源）。
package pgstore

import (
	"context"
	"testing"
	"time"
)

func TestListFundingWitnessedSymbols(t *testing.T) {
	s := carryUniverseTestStore(t) // 复用迁移+隔离夹具
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM facts WHERE kind='funding'`); err != nil {
		t.Fatal(err)
	}
	ins := func(venue, sym string, age time.Duration) {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO facts (kind, venue, symbol, value, ts) VALUES ('funding',$1,$2,0.1,now()-$3::interval)`,
			venue, sym, age.String()); err != nil {
			t.Fatal(err)
		}
	}
	ins("binance", "BTCUSDT", time.Hour)
	ins("binance", "ETHUSDT", 30*time.Minute)
	ins("gate", "XAUUSDT", time.Hour)
	ins("okx", "OLDUSDT", 7*24*time.Hour)     // 窗外不取
	ins("binance", "BTCUSDT", 10*time.Minute) // 重复去重

	got, err := s.ListFundingWitnessedSymbols(ctx, "binance", 6*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "BTCUSDT" || got[1] != "ETHUSDT" {
		t.Fatalf("binance 见证集 = %v, want [BTCUSDT ETHUSDT]（分所+窗+去重保序）", got)
	}
	got, err = s.ListFundingWitnessedSymbols(ctx, "okx", 6*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("okx 窗外符号不应取，got %v", got)
	}
}
