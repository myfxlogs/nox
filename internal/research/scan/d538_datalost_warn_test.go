// d538_datalost_warn_test.go —— D-538①：data_lost 批量退聚合 warn——连缺 3 轮退行
// → 一轮至多一条聚合告警落 alerts（D-532② 断链退仓静默病灶补口）。
package scan

import (
	"context"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

func TestD538_SpotDataLostAggregateWarn(t *testing.T) {
	s := d534Store(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `DELETE FROM alerts`); err != nil {
		t.Fatalf("alerts clean: %v", err)
	}
	for _, sym := range []string{"DLAUSDT", "DLBUSDT"} {
		if err := s.InsertPaperSpotCarry(ctx, &pgstore.PaperSpotCarry{
			VenueA: "binance", Symbol: sym, Side: "short_perp",
			EntryFundingAnn: 30, QtyUSD: 1000, Structure: "spot_carry",
		}); err != nil {
			t.Fatalf("seed %s: %v", sym, err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, _, err := StepPaperSpotCarry(ctx, s, NewEntryStreak(), time.Now(), nil); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
	}
	var warns int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='warn' AND message LIKE '%data_lost%'`).Scan(&warns); err != nil {
		t.Fatalf("alerts: %v", err)
	}
	if warns != 1 {
		t.Fatalf("聚合 warn 应恰一条: got %d", warns)
	}
	var msg string
	if err := s.Pool().QueryRow(ctx,
		`SELECT message FROM alerts WHERE level='warn' AND message LIKE '%data_lost%' LIMIT 1`).Scan(&msg); err != nil {
		t.Fatalf("read: %v", err)
	}
	if msg == "" || !containsAll(msg, "spot", "2 行", "DLAUSDT", "DLBUSDT") {
		t.Fatalf("warn 内容不符: %q", msg)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		hit := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}
