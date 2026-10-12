package position

// D-469b T3：Monitor/PersonalMonitor KickTopic——positions 变更事件 → loadPositions 即刷。

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

func testPGStore(t *testing.T) *pgstore.Store {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return st
}

// T3：kick → loadPositions 立即执行（ticker=5min 等不到，唯一触发路=kick）。
func TestD469b_MonitorKickRefresh(t *testing.T) {
	st := testPGStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := st.Pool().Exec(ctx, "TRUNCATE positions CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	topic := bus.NewTopic[struct{}]()
	m := New(nil, st, 0, time.Minute)
	m.SetKickTopic(topic)
	// refreshLoop 单跑（Run 会订 QuoteBus——nil bus 不安全，直驱 refreshLoop）。
	go m.refreshLoop(ctx)
	time.Sleep(50 * time.Millisecond) // 订阅建立

	id, err := st.CreatePosition(ctx, &pgstore.Position{Type: "CROSS_VENUE", Status: "OPEN"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	topic.Publish(struct{}{})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		_, ok := m.positions[id]
		m.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("kick 后 positions 未含新建持仓 #%d（5min ticker 不可能到达）", id)
}
