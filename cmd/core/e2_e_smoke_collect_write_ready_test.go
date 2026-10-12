// d512_e2e_smoke_test.go —— D-512 T10 增补：关键链路启停级冒烟（e2e 冒烟扩面）。
// 覆盖=①readyz 分层装配可执行（DB 真连）②facts 采集→落库→查询写入路径绿。
// compose 起→healthz 200 级别的部署面冒烟属部署侧，仓内以同构用例固化。
package main

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"
	"nox/internal/platform/config"
	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

// TestD512_E2E_Smoke_CollectWriteReady 启停级冒烟：DB 写入路径（facts 采集落库→查询）
// + readyz 分层结构可执行——关键链路「采集→落库→读回」一段绿。
func TestD512_E2E_Smoke_CollectWriteReady(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()

	// ①写入路径：facts 批写（采集器 Sink 同路径）。
	ts := time.Now().UTC()
	facts := []fact.Fact{
		{Kind: fact.KindFunding, Venue: "binance", Symbol: "D512SMKUSDT", Value: 2.5, Unit: fact.UnitPctAnnualized, Ts: ts, Src: "d512_smoke"},
	}
	if err := st.InsertFacts(ctx, facts); err != nil {
		t.Fatalf("facts 写入: %v", err)
	}
	t.Cleanup(func() { st.Pool().Exec(ctx, `DELETE FROM facts WHERE src='d512_smoke'`) })

	// ②读回：FactsSince 能读到刚落的 funding fact（采集→落库→查询链绿）。
	rows, err := st.FactsSince(ctx, fact.KindFunding, "binance", "D512SMKUSDT", ts.Add(-time.Minute))
	if err != nil {
		t.Fatalf("facts 读回: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("写入的 funding fact 读不回（落库/查询链断裂）")
	}

	// ③readyz 分层结构：DB 层真 ping 可执行且 ready（qbus 空+cfg 最小面）。
	rep := readyzCheck(ctx, st, bus.New(), &config.Config{DetectorFreshnessTTL: time.Minute}, nil, nil)
	if !rep.DatabaseReady.Ready {
		t.Fatalf("database_ready 应绿: %+v", rep.DatabaseReady)
	}
	if !rep.ProcessAlive.Ready {
		t.Fatal("process_alive 应绿")
	}
}
