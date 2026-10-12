package scan

// 测试共享基建：结算边界事实种子（S1 起累积依赖 funding_next_settle/
// funding_interval facts——无事实=fail-closed 零累积，旧墙钟测试须补种）。
import (
	"context"
	"testing"
	"time"

	"arb/internal/data/fact"
	"arb/internal/store/pgstore"
)

// seedSettleFacts 给 (venue, symbol) 种 8h UTC 网格的结算边界事实：
// next_settle=now 后下一个 8h 界，interval=28800s。无 funding_history 行时
// 每界回落当期率（CurRate）——恒定率下与旧墙钟公式同值（48h=6 界恒成立）。
func seedSettleFacts(t *testing.T, s *pgstore.Store, venue, symbol string, now time.Time) {
	t.Helper()
	iv := int64(8 * 3600)
	grid := now.Unix() / iv * iv
	ns := time.Unix(grid+iv, 0)
	err := s.InsertFacts(context.Background(), []fact.Fact{
		{Kind: fact.KindFundingNextSettle, Venue: venue, Symbol: symbol, Value: float64(ns.Unix()), Unit: fact.UnitUnixSec, Ts: now},
		{Kind: fact.KindFundingInterval, Venue: venue, Symbol: symbol, Value: float64(iv), Unit: fact.UnitSeconds, Ts: now},
	})
	if err != nil {
		t.Fatalf("seed settle facts %s|%s: %v", venue, symbol, err)
	}
}
