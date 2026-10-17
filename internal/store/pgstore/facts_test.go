// facts_test.go —— facts 读件锚（棘轮拆出件零行为变）。
package pgstore

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestLatestTickMidWithin D-604 N2 锚：新鲜 tick 过/陈旧 tick fail-closed 拒。
func TestLatestTickMidWithin(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	// 建本测试期分区（ticks 分区表——prune 测试先例自建当月分区间）
	m := time.Now().UTC()
	first := time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, time.UTC)
	next := first.AddDate(0, 1, 0)
	pname := fmt.Sprintf("ticks_%d_%02d", first.Year(), int(first.Month()))
	if _, err := st.Pool().Exec(ctx, fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s PARTITION OF ticks FOR VALUES FROM ('%s') TO ('%s')`,
		pname, first.Format("2006-01-02"), next.Format("2006-01-02"))); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO ticks (venue, venue_type, symbol, bid, ask, ts) VALUES
		('gate','perp','GATEFRESH',99,101,now()-interval '1 minute'),
		('gate','perp','GATESTALE',99,101,now()-interval '90 minutes')`); err != nil {
		t.Fatal(err)
	}
	if v, ok := st.LatestTickMidWithin(ctx, "gate", "GATEFRESH", 30*time.Minute); !ok || v != 100 {
		t.Fatalf("新鲜 tick 拒/值错：%v %v", v, ok)
	}
	if _, ok := st.LatestTickMidWithin(ctx, "gate", "GATESTALE", 30*time.Minute); ok {
		t.Fatal("陈旧 tick 应 fail-closed 拒")
	}
	// 无窗（maxAge=0）保旧语义——陈旧也过
	if v, ok := st.LatestTickMidWithin(ctx, "gate", "GATESTALE", 0); !ok || v != 100 {
		t.Fatalf("无窗行拒：%v %v", v, ok)
	}
}
