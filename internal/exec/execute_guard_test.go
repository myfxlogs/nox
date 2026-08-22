package exec

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"arb/internal/store/pgstore"
	"github.com/jackc/pgx/v5"
)

// testStore 连接 ARB_EXEC_TEST_PG_DSN 指定的**专用测试库**（未设置时跳过）。
// 安全闸：dbname 必须含 "test"（可销毁数据），否则拒绝。Migrate 幂等建全量 schema。
// 专用库原因（D-058）：pgstore 测试用 arb_test 且其 prepareTestSchema 会 DROP SCHEMA public
// CASCADE（sync.Once 每进程一次）；go test 默认 -p 并发跑包，exec 若共用 arb_test 会与
// pgstore 的并发 DDL 竞态（见 23505/no schema 报错）。exec 用自己的库 = 零跨包 DDL 冲突。
func testStore(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := os.Getenv("ARB_EXEC_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("ARB_EXEC_TEST_PG_DSN 未设置，跳过需真库的测试")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse ARB_TEST_PG_DSN: %v", err)
	}
	if !strings.Contains(cfg.Database, "test") {
		t.Fatalf("ARB_TEST_PG_DSN 指向非测试库 %q（测试会 TRUNCATE，拒绝）", cfg.Database)
	}
	ctx := context.Background()
	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

// seedConn 用直连 pgx 播种 positions（exec 测试种子 SQL，绕过 Store 未导出的 pool）。
func seedConn(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgx.Connect: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	if _, err := conn.Exec(context.Background(), `TRUNCATE positions CASCADE`); err != nil {
		t.Fatalf("truncate positions: %v", err)
	}
	return conn
}

// TestExecute_GuardMaxOpenPositions 验证 MaxOpenPositions 风控守卫（D-058）：
// 持仓数 ≥ 上限时 Execute 在创建持仓前拒单，返回 ErrMaxOpenPositions（不触达下单）。
func TestExecute_GuardMaxOpenPositions(t *testing.T) {
	dsn := os.Getenv("ARB_EXEC_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("ARB_EXEC_TEST_PG_DSN 未设置，跳过")
	}
	s := testStore(t)
	conn := seedConn(t, dsn)
	ctx := context.Background()

	// 播种 1 条 OPEN 持仓，上限设 1 → 计数 1 ≥ 1 → 拒单。
	if _, err := conn.Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ('opp-seed','CROSS_VENUE','OPEN',0)`); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	p := &Pipeline{store: s, limits: RiskLimits{MaxOpenPositions: 1}}
	_, err := p.Execute(ctx, "opp-new", "req-1", []LegSpec{{VenueKey: "v:mt5", VenueSymbol: "X", Side: "BUY", Qty: 1}})
	if !errors.Is(err, ErrMaxOpenPositions) {
		t.Fatalf("want ErrMaxOpenPositions, got %v", err)
	}

	// 上限 0 = 不限（默认关闭守卫）。legs=nil 不触达 registry，走 FAILED 收尾。
	p0 := &Pipeline{store: s, limits: RiskLimits{MaxOpenPositions: 0}}
	_, err = p0.Execute(ctx, "opp-new", "req-1", nil)
	if err != nil {
		t.Fatalf("MaxOpenPositions=0 want unlimited (no guard), got %v", err)
	}
}

// TestExecute_GuardDuplicate 验证幂等防重守卫（D-058）：
// 同机会已有未平仓持仓（OPEN）→ ErrDuplicateExecution；无持仓 → 正常放行。
func TestExecute_GuardDuplicate(t *testing.T) {
	dsn := os.Getenv("ARB_EXEC_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("ARB_EXEC_TEST_PG_DSN 未设置，跳过")
	}
	s := testStore(t)
	conn := seedConn(t, dsn)
	ctx := context.Background()

	// 同机会已有 OPEN 持仓 → 拒重（守卫在任何 legs 触达前返回）。
	if _, err := conn.Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ('opp-x','CROSS_VENUE','OPEN',0)`); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	p := &Pipeline{store: s, limits: RiskLimits{MaxOpenPositions: 10}}
	_, err := p.Execute(ctx, "opp-x", "req-2", []LegSpec{{VenueKey: "v:mt5", VenueSymbol: "X", Side: "BUY", Qty: 1}})
	if !errors.Is(err, ErrDuplicateExecution) {
		t.Fatalf("want ErrDuplicateExecution, got %v", err)
	}

	// 同机会 FAILED（可重入）→ 放行到建仓，不返回 ErrDuplicateExecution。
	// legs=nil 不触达 registry，走 FAILED 收尾。
	if _, err := conn.Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ('opp-failed','CROSS_VENUE','FAILED',0)`); err != nil {
		t.Fatalf("seed failed position: %v", err)
	}
	_, err = p.Execute(ctx, "opp-failed", "req-3", nil)
	if errors.Is(err, ErrDuplicateExecution) {
		t.Fatal("FAILED position want re-entry allowed, got ErrDuplicateExecution")
	}
}
