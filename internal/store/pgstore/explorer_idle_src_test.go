// explorer_idle_src_test.go —— 读序权威锚（业主令 10-15 事件驱动升权威源）：
// 同键同窗事件行（okx_event）优先于 rest_calib；事件行陈于 15min 窗才落 rest。
package pgstore

import (
	"context"
	"testing"
	"time"
)

func TestExplorerIdle_EventSrcPriority(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	ins := func(src string, eq float64, at time.Time) {
		_, err := s.pool.Exec(ctx, `INSERT INTO fin_venue_account_snapshot
			(venue,venue_type,balance_raw,equity_raw,currency,fetched_at,src) VALUES
			('okx','unified',$1,$1,'USDT',$2,$3)`, eq, at, src)
		if err != nil {
			t.Fatal(err)
		}
	}
	// 事件行 3.4e-9（真 USDT 尘埃） vs rest_calib 19.95（化石伪标）同窗共存
	ins("okx_event", 0.0000000034, now.Add(-time.Minute))
	ins("rest_calib", 19.95, now)
	pools, err := s.ExplorerIdleFunds(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	var eq float64
	var found bool
	for _, p := range pools {
		if p.Venue == "okx" && p.VenueType == "unified" && p.Currency == "USDT" {
			eq, found = p.Equity, true
		}
	}
	if !found || eq > 0.001 {
		t.Fatalf("事件行未优先：eq=%v found=%v（want ~3.4e-9）", eq, found)
	}
	// 事件行陈于 15min 窗 → rest_calib 补位
	if _, err := s.pool.Exec(ctx, `UPDATE fin_venue_account_snapshot SET fetched_at=$1
		WHERE venue='okx' AND src='okx_event'`, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	pools, err = s.ExplorerIdleFunds(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pools {
		if p.Venue == "okx" && p.VenueType == "unified" && p.Currency == "USDT" {
			if p.Equity != 19.95 {
				t.Fatalf("陈旧事件行应让位 rest_calib：eq=%v want 19.95", p.Equity)
			}
			return
		}
	}
	t.Fatal("池行缺失")
}
