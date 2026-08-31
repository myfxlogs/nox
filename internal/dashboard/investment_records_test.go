package dashboard

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
)

// testServiceStore 连接 ARB_TEST_PG_SERVICE_DSN 指定的专用测试库（与 pgstore 包测试库隔离，避免并行 TRUNCATE 竞态）。
// 未设置时跳过；dbname 必须含 "test"（安全闸）。
// schema 用 D-054 根治模式：每测试进程 DROP schema + 全量 Migrate（v1..N 真执行），与生产完全同态。
func testServiceStore(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := os.Getenv("ARB_TEST_PG_SERVICE_DSN")
	if dsn == "" {
		t.Skip("ARB_TEST_PG_SERVICE_DSN 未设置，跳过需真库的测试")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse ARB_TEST_PG_SERVICE_DSN: %v", err)
	}
	if !strings.Contains(cfg.Database, "test") {
		t.Fatalf("ARB_TEST_PG_SERVICE_DSN 指向非测试库 %q：测试会 TRUNCATE 该库，拒绝执行", cfg.Database)
	}
	// D-177 CI 修复：pgstore.New 用 30s 超时（防 fd 压力下挂起），
	// 但 TRUNCATE 用独立 context（schema 重建可能消耗大部分时间）。
	newCtx, newCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer newCancel()
	s, err := pgstore.New(newCtx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	rebuildServiceTestSchema(t, s)
	// 每测试函数清财务线表（数据隔离；schema 重建每进程仅一次）。
	truncCtx, truncCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer truncCancel()
	if _, err := s.Pool().Exec(truncCtx, "TRUNCATE investment_records, position_daily_income, personal_positions CASCADE"); err != nil {
		t.Fatalf("TRUNCATE investment_records, position_daily_income, personal_positions: %v", err)
	}
	return s
}

// rebuildServiceTestSchemaOnce 每个测试进程只重建一次服务测试库 schema。
var rebuildServiceTestSchemaOnce sync.Once

// rebuildServiceTestSchema DROP schema + 全量 Migrate，使服务测试库 schema 恒等于生产最新
// （同 pgstore.prepareTestSchema，D-054 根治）。旧实现把 v1..7 预标记「已应用」而不物理执行其
// SQL → 每新增一个迁移边界就要前移一位，前序表（如 0012 canonical 引用的 broker_symbol_map）
// 在测试库物理缺失 → 全量 go test 崩（KB: backfill-update-test-seed-rows，复发 3 次）。
func rebuildServiceTestSchema(t *testing.T, s *pgstore.Store) {
	t.Helper()
	ctx := context.Background()
	var rebuildErr error
	rebuildServiceTestSchemaOnce.Do(func() {
		if _, err := s.Pool().Exec(ctx, `DROP SCHEMA public CASCADE`); err != nil {
			rebuildErr = fmt.Errorf("drop schema: %w", err)
			return
		}
		if _, err := s.Pool().Exec(ctx, `CREATE SCHEMA public`); err != nil {
			rebuildErr = fmt.Errorf("create schema: %w", err)
			return
		}
		if err := s.Migrate(ctx); err != nil {
			rebuildErr = fmt.Errorf("migrate: %w", err)
			return
		}
	})
	if rebuildErr != nil {
		t.Fatalf("服务测试库重建失败: %v", rebuildErr)
	}
}

// TestAddInvestmentRecord_InvalidKind：kind 非法 → connect CodeInvalidArgument（store 为空也能拦住，后端校验在前）。
func TestAddInvestmentRecord_InvalidKind(t *testing.T) {
	svc := &Service{}
	ctx := context.Background()
	for _, kind := range []string{"", "bad", "DECISION", "Observation"} {
		_, err := svc.AddInvestmentRecord(ctx, connect.NewRequest(&v1.AddInvestmentRecordRequest{
			Kind: kind, Asset: "TRX", Content: "x",
		}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("kind %q want CodeInvalidArgument, got %v", kind, err)
		}
	}
}

// TestAddInvestmentRecord_ContentRequired：content 空/纯空白 → 拒绝；合法 kind + 有 content 才走到 store。
func TestAddInvestmentRecord_ContentRequired(t *testing.T) {
	svc := &Service{}
	ctx := context.Background()
	for _, content := range []string{"", "   ", "\n\t"} {
		_, err := svc.AddInvestmentRecord(ctx, connect.NewRequest(&v1.AddInvestmentRecordRequest{
			Kind: "decision", Asset: "TRX", Content: content,
		}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("content %q want CodeInvalidArgument, got %v", content, err)
		}
	}
}

// TestListInvestmentRecords_ParamsPassthrough：筛选/搜索/limit 参数透传至后端 SQL（D-043：前端只传条件，后端过滤）。
func TestListInvestmentRecords_ParamsPassthrough(t *testing.T) {
	s := testServiceStore(t)
	svc := &Service{store: s}
	ctx := context.Background()

	// 经 handler 写入 3 条（覆盖两 kind + 两 asset）
	seeds := []*v1.AddInvestmentRecordRequest{
		{Kind: "decision", Asset: "TRX", Content: "质押 14 天解绑，能量收益观察"},
		{Kind: "lesson", Asset: "TRX", Content: "强平底线 0.3695，跌破手动离场"},
		{Kind: "observation", Asset: "USDT", Content: "资金费率转正，机会窗口"},
	}
	for i, m := range seeds {
		resp, err := svc.AddInvestmentRecord(ctx, connect.NewRequest(m))
		if err != nil {
			t.Fatalf("AddInvestmentRecord #%d: %v", i, err)
		}
		if resp.Msg.Id <= 0 {
			t.Errorf("AddInvestmentRecord #%d returned id want > 0, got %d", i, resp.Msg.Id)
		}
	}

	// 空筛选 → 全量 3
	all, err := svc.ListInvestmentRecords(ctx, connect.NewRequest(&v1.ListInvestmentRecordsRequest{}))
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(all.Msg.Items) != 3 {
		t.Errorf("empty filter want 3 items, got %d", len(all.Msg.Items))
	}

	// kind 透传
	lesson, err := svc.ListInvestmentRecords(ctx, connect.NewRequest(&v1.ListInvestmentRecordsRequest{Kind: "lesson"}))
	if err != nil {
		t.Fatalf("List kind: %v", err)
	}
	if len(lesson.Msg.Items) != 1 || lesson.Msg.Items[0].Kind != "lesson" {
		t.Errorf("kind=lesson want 1 lesson item, got %d", len(lesson.Msg.Items))
	}

	// asset 子串透传（ILIKE）
	trx, err := svc.ListInvestmentRecords(ctx, connect.NewRequest(&v1.ListInvestmentRecordsRequest{Asset: "trx"}))
	if err != nil {
		t.Fatalf("List asset: %v", err)
	}
	if len(trx.Msg.Items) != 2 {
		t.Errorf("asset 'trx' want 2 items, got %d", len(trx.Msg.Items))
	}

	// q 内容关键词透传
	byQ, err := svc.ListInvestmentRecords(ctx, connect.NewRequest(&v1.ListInvestmentRecordsRequest{Q: "离场"}))
	if err != nil {
		t.Fatalf("List q: %v", err)
	}
	if len(byQ.Msg.Items) != 1 || byQ.Msg.Items[0].Content != "强平底线 0.3695，跌破手动离场" {
		t.Errorf("q '离场' want 1 hit, got %d", len(byQ.Msg.Items))
	}

	// limit 透传
	lim, err := svc.ListInvestmentRecords(ctx, connect.NewRequest(&v1.ListInvestmentRecordsRequest{Limit: 2}))
	if err != nil {
		t.Fatalf("List limit: %v", err)
	}
	if len(lim.Msg.Items) != 2 {
		t.Errorf("limit 2 want 2 items, got %d", len(lim.Msg.Items))
	}

	// 字段映射完整（id/kind/asset/content/ts 非零）
	if len(all.Msg.Items) > 0 {
		it := all.Msg.Items[0]
		if it.Id <= 0 || it.Kind == "" || it.Content == "" || it.Ts == nil {
			t.Errorf("item fields incomplete: %+v", it)
		}
	}
}
