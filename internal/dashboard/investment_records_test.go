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

// serviceTestStoreOnce 包级单例 store——所有 SERVICE_DSN 测试共用一个 pool，避免
// 每测试 pgstore.New 创建独立 pool 累积连接耗尽 pg 服务端 max_connections（D-212 CI 红根因：
// pool_max_conns=2 × N 测试 → Acquire 卡住 → 30s 超时 flaky）。
var serviceTestStoreOnce sync.Once
var serviceTestStoreSingle *pgstore.Store

// serviceTestStoreSingleInit 包级单例 store 初始化（sync.Once，进程结束自动 Close）。
func serviceTestStoreSingleInit(t *testing.T) *pgstore.Store {
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
	var initErr error
	serviceTestStoreOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s, err := pgstore.New(ctx, dsn)
		if err != nil {
			initErr = fmt.Errorf("pgstore.New: %w", err)
			return
		}
		if err := s.Migrate(ctx); err != nil {
			initErr = fmt.Errorf("migrate: %w", err)
			return
		}
		serviceTestStoreSingle = s
	})
	if initErr != nil {
		t.Fatalf("serviceTestStore init: %v", initErr)
	}
	if serviceTestStoreSingle == nil {
		t.Skip("ARB_TEST_PG_SERVICE_DSN 未设置，跳过需真库的测试")
	}
	return serviceTestStoreSingle
}

// testServiceStore 连接 ARB_TEST_PG_SERVICE_DSN 指定的专用测试库（与 pgstore 包测试库隔离，避免并行 TRUNCATE 竞态）。
// 未设置时跳过；dbname 必须含 "test"（安全闸）。
// D-212 CI 红根因修复：改为包级单例 pool（serviceTestStoreSingleInit），避免每测试 New 独立 pool
// 累积连接耗尽 pg 服务端 max_connections → Acquire 卡住 → 30s 超时 flaky。
func testServiceStore(t *testing.T) *pgstore.Store {
	t.Helper()
	s := serviceTestStoreSingleInit(t)
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

// rebuildServiceTestSchema 保留兼容签名（no-op，单例 store 已在 serviceTestStoreSingleInit 内 Migrate）。
// D-212 CI 红根因修复：原 DROP SCHEMA + Migrate 改为单例 pool + 一次性 Migrate，此函数仅占位。
func rebuildServiceTestSchema(t *testing.T, s *pgstore.Store) {
	t.Helper()
	// no-op：schema 由 serviceTestStoreSingleInit 的 sync.Once Migrate 保证。
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
