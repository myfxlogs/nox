// D-292 S3 测试：价格提醒规则管理面 service handler（校验 + source_matched + 全生命周期）。
package dashboard

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
	"arb/internal/testutil"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// priceAlertSvcTest 连接 ARB_TEST_PG_DSN，构造 Service（含 qbus）+ 清表。
func priceAlertSvcTest(t *testing.T) *Service {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(st.Close)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, "TRUNCATE price_alerts RESTART IDENTITY"); err != nil {
		t.Fatalf("TRUNCATE price_alerts: %v", err)
	}
	return &Service{store: st, qbus: bus.New()}
}

// TestCreatePriceAlertRule_Validation 六类非法输入逐个拒绝（先红后绿：校验在前，DB 不应被触达）。
func TestCreatePriceAlertRule_Validation(t *testing.T) {
	s := priceAlertSvcTest(t)
	ctx := context.Background()
	cases := []struct {
		name      string
		symbol    string
		op        string
		threshold float64
		level     string
		message   string
	}{
		{"symbol 空", "", ">=", 0.34, "warn", "msg"},
		{"symbol 超长", "TRXUSDT1234567890X", ">=", 0.34, "warn", "msg"},
		{"op 非法", "TRXUSDT", ">", 0.34, "warn", "msg"},
		{"threshold 零", "TRXUSDT", ">=", 0, "warn", "msg"},
		{"level 非法", "TRXUSDT", ">=", 0.34, "panic", "msg"},
		{"message 空", "TRXUSDT", ">=", 0.34, "warn", "  "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := s.CreatePriceAlertRule(ctx, connect.NewRequest(&v1.CreatePriceAlertRuleRequest{
				Symbol: c.symbol, Op: c.op, Threshold: c.threshold, Level: c.level, Message: c.message,
			}))
			if err != nil {
				t.Fatalf("CreatePriceAlertRule 返回 transport err: %v", err)
			}
			if resp.Msg.Ok {
				t.Fatalf("非法输入 %q 应被拒绝，got ok=true", c.name)
			}
			if resp.Msg.Error == "" {
				t.Fatalf("非法输入 %q 应返回 error 文案，got 空", c.name)
			}
		})
	}
	// 校验失败不应写入任何行
	list, err := s.store.ListPriceAlertRules(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("校验失败不应写入 DB，got %d 行", len(list))
	}
}

// TestCreatePriceAlertRule_SymbolNormalized symbol 去空格+转大写。
func TestCreatePriceAlertRule_SymbolNormalized(t *testing.T) {
	s := priceAlertSvcTest(t)
	ctx := context.Background()
	resp, err := s.CreatePriceAlertRule(ctx, connect.NewRequest(&v1.CreatePriceAlertRuleRequest{
		Symbol: "  trxusdt  ", Op: ">=", Threshold: 0.34, Level: "warn", Message: "TRX 涨到 0.34",
	}))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !resp.Msg.Ok {
		t.Fatalf("ok=false, error=%s", resp.Msg.Error)
	}
	if resp.Msg.Rule.Symbol != "TRXUSDT" {
		t.Fatalf("symbol 应规范化为 TRXUSDT，got %q", resp.Msg.Rule.Symbol)
	}
}

// TestPriceAlertRule_SourceMatched source_matched：symbol 在 QuoteBus 已发布 → true；未发布 → false。
func TestPriceAlertRule_SourceMatched(t *testing.T) {
	s := priceAlertSvcTest(t)
	ctx := context.Background()
	// 发布 TRXUSDT 报价到 qbus
	s.qbus.Publish(bus.Quote{Symbol: "TRXUSDT", Venue: "binance", VenueType: "spot", Bid: 0.33, Ask: 0.33})

	// 已覆盖 symbol → source_matched=true
	r1, err := s.CreatePriceAlertRule(ctx, connect.NewRequest(&v1.CreatePriceAlertRuleRequest{
		Symbol: "TRXUSDT", Op: ">=", Threshold: 0.34, Level: "warn", Message: "TRX 涨到 0.34",
	}))
	if err != nil {
		t.Fatalf("Create r1: %v", err)
	}
	if !r1.Msg.Rule.SourceMatched {
		t.Fatalf("TRXUSDT 已在 QuoteBus 发布，source_matched 应 true")
	}

	// 未覆盖 symbol（typo）→ source_matched=false
	r2, err := s.CreatePriceAlertRule(ctx, connect.NewRequest(&v1.CreatePriceAlertRuleRequest{
		Symbol: "TRXUSDTX", Op: ">=", Threshold: 0.34, Level: "warn", Message: "typo 规则",
	}))
	if err != nil {
		t.Fatalf("Create r2: %v", err)
	}
	if r2.Msg.Rule.SourceMatched {
		t.Fatalf("TRXUSDTX 未在 QuoteBus 发布，source_matched 应 false")
	}
}

// TestPriceAlertRule_Lifecycle 全生命周期：Create→List→Update（启停+改字段）→Delete。
func TestPriceAlertRule_Lifecycle(t *testing.T) {
	s := priceAlertSvcTest(t)
	ctx := context.Background()

	// Create
	r, err := s.CreatePriceAlertRule(ctx, connect.NewRequest(&v1.CreatePriceAlertRuleRequest{
		Symbol: "TRXUSDT", Op: ">=", Threshold: 0.34, Level: "warn", Message: "TRX 涨到 0.34",
	}))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !r.Msg.Ok || r.Msg.Rule.Id <= 0 {
		t.Fatalf("Create 失败: ok=%v rule=%+v", r.Msg.Ok, r.Msg.Rule)
	}
	id := r.Msg.Rule.Id

	// List
	list, err := s.ListPriceAlertRules(ctx, connect.NewRequest(&v1.ListPriceAlertRulesRequest{}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Msg.Rules) != 1 || list.Msg.Rules[0].Id != id {
		t.Fatalf("List 应含 1 条 id=%d，got %+v", id, list.Msg.Rules)
	}

	// Update：停用 + 改阈值 + 改文案
	upd, err := s.UpdatePriceAlertRule(ctx, connect.NewRequest(&v1.UpdatePriceAlertRuleRequest{
		Id: id, Symbol: "TRXUSDT", Op: ">=", Threshold: 0.40, Level: "critical", Message: "TRX 涨到 0.40", Enabled: false,
	}))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !upd.Msg.Ok {
		t.Fatalf("Update ok=false, error=%s", upd.Msg.Error)
	}
	if upd.Msg.Rule.Threshold != 0.40 || upd.Msg.Rule.Level != "critical" || upd.Msg.Rule.Enabled {
		t.Fatalf("Update 字段未生效: %+v", upd.Msg.Rule)
	}

	// List 验证停用
	list2, _ := s.ListPriceAlertRules(ctx, connect.NewRequest(&v1.ListPriceAlertRulesRequest{}))
	if list2.Msg.Rules[0].Enabled {
		t.Fatalf("停用后 List 应 enabled=false")
	}

	// Delete
	del, err := s.DeletePriceAlertRule(ctx, connect.NewRequest(&v1.DeletePriceAlertRuleRequest{Id: id}))
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !del.Msg.Ok {
		t.Fatalf("Delete ok=false, error=%s", del.Msg.Error)
	}

	// List 空
	list3, _ := s.ListPriceAlertRules(ctx, connect.NewRequest(&v1.ListPriceAlertRulesRequest{}))
	if len(list3.Msg.Rules) != 0 {
		t.Fatalf("Delete 后应 0 行，got %d", len(list3.Msg.Rules))
	}

	// Delete 不存在的 id
	del2, _ := s.DeletePriceAlertRule(ctx, connect.NewRequest(&v1.DeletePriceAlertRuleRequest{Id: id}))
	if del2.Msg.Ok {
		t.Fatalf("Delete 不存在 id 应 ok=false")
	}
}

// TestUpdatePriceAlertRule_InvalidId id<=0 拒绝。
func TestUpdatePriceAlertRule_InvalidId(t *testing.T) {
	s := priceAlertSvcTest(t)
	ctx := context.Background()
	resp, err := s.UpdatePriceAlertRule(ctx, connect.NewRequest(&v1.UpdatePriceAlertRuleRequest{
		Id: 0, Symbol: "TRXUSDT", Op: ">=", Threshold: 0.34, Level: "warn", Message: "msg", Enabled: true,
	}))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if resp.Msg.Ok {
		t.Fatalf("id=0 应被拒绝")
	}
}
