package dashboard

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/bus"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"connectrpc.com/connect"
)

// TestAddDailyIncome_InvalidDay：day 非法 → CodeInvalidArgument（含非严格 YYYY-MM-DD，如 2026-8-17）。
func TestAddDailyIncome_InvalidDay(t *testing.T) {
	svc := &Service{}
	ctx := context.Background()
	for _, day := range []string{"", "2026-8-17", "2026/08/17", "2026-08-32", "20260817", "2026-08-17T00:00:00Z"} {
		_, err := svc.AddDailyIncome(ctx, connect.NewRequest(&v1.AddDailyIncomeRequest{Day: day}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("day %q want CodeInvalidArgument, got %v", day, err)
		}
	}
}

// TestAddDailyIncome_AutoFill：合法写入 → 后端自动取价写 price、算 pnl_usd；List 读回 income_usd 后端算。
func TestAddDailyIncome_AutoFill(t *testing.T) {
	s := testServiceStore(t)
	ctx := context.Background()
	qbus := bus.New()
	now := time.Now()
	qbus.Publish(bus.Quote{Symbol: "TRXUSDT", Venue: "binance", VenueType: "spot", Bid: 0.33, Ask: 0.33, Time: now})
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}

	resp, err := svc.AddDailyIncome(ctx, connect.NewRequest(&v1.AddDailyIncomeRequest{
		Day: "2026-08-17", EnergyTrx: 5, VotingTrx: 1, InterestUsd: 0, Note: "能量+投票",
	}))
	if err != nil {
		t.Fatalf("AddDailyIncome: %v", err)
	}
	if resp.Msg.Id <= 0 {
		t.Errorf("id want > 0, got %d", resp.Msg.Id)
	}

	list, err := svc.ListDailyIncome(ctx, connect.NewRequest(&v1.ListDailyIncomeRequest{}))
	if err != nil {
		t.Fatalf("ListDailyIncome: %v", err)
	}
	if len(list.Msg.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(list.Msg.Items))
	}
	it := list.Msg.Items[0]
	if it.Day != "2026-08-17" {
		t.Errorf("day want 2026-08-17, got %q", it.Day)
	}
	if it.Price != 0.33 {
		t.Errorf("price want auto-filled 0.33, got %v", it.Price)
	}
	wantPnl := (0 - 0.33) * 0
	if math.Abs(it.PnlUsd-wantPnl) > 1e-9 {
		t.Errorf("pnl want %v, got %v", wantPnl, it.PnlUsd)
	}
	wantIncome := 5*0.33 + 1*0.33 + 0
	if math.Abs(it.IncomeUsd-wantIncome) > 1e-9 {
		t.Errorf("income_usd want %v, got %v", wantIncome, it.IncomeUsd)
	}
	if it.Note != "能量+投票" {
		t.Errorf("note want 能量+投票, got %q", it.Note)
	}
}

// TestAddDailyIncome_SameDayUpsert：同 day 重复 → upsert 覆盖（值更新、不新增行、id 不变）。
func TestAddDailyIncome_SameDayUpsert(t *testing.T) {
	s := testServiceStore(t)
	ctx := context.Background()
	svc := &Service{store: s, qbus: bus.New(), freshTTL: time.Minute, liqMMR: 0.005}

	r1, err := svc.AddDailyIncome(ctx, connect.NewRequest(&v1.AddDailyIncomeRequest{Day: "2026-08-17", EnergyTrx: 5, VotingTrx: 1, InterestUsd: 0}))
	if err != nil {
		t.Fatalf("first Add: %v", err)
	}
	r2, err := svc.AddDailyIncome(ctx, connect.NewRequest(&v1.AddDailyIncomeRequest{Day: "2026-08-17", EnergyTrx: 6, VotingTrx: 2, InterestUsd: 2.0}))
	if err != nil {
		t.Fatalf("second Add: %v", err)
	}
	if r2.Msg.Id != r1.Msg.Id {
		t.Errorf("upsert want same id %d, got %d", r1.Msg.Id, r2.Msg.Id)
	}

	list, err := svc.ListDailyIncome(ctx, connect.NewRequest(&v1.ListDailyIncomeRequest{}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Msg.Items) != 1 {
		t.Fatalf("upsert want 1 item, got %d", len(list.Msg.Items))
	}
	it := list.Msg.Items[0]
	if it.EnergyTrx != 6 || it.VotingTrx != 2 || it.InterestUsd != 2.0 {
		t.Errorf("upsert values not updated: %+v", it)
	}
}

// TestAddDailyIncome_NoQuote：无报价 → price/pnl 存 NULL（List 读回 0，诚实空态不写 0 冒充现价）。
func TestAddDailyIncome_NoQuote(t *testing.T) {
	s := testServiceStore(t)
	ctx := context.Background()
	svc := &Service{store: s, qbus: bus.New(), freshTTL: time.Minute, liqMMR: 0.005}

	if _, err := svc.AddDailyIncome(ctx, connect.NewRequest(&v1.AddDailyIncomeRequest{Day: "2026-08-17", EnergyTrx: 5})); err != nil {
		t.Fatalf("Add: %v", err)
	}
	list, err := svc.ListDailyIncome(ctx, connect.NewRequest(&v1.ListDailyIncomeRequest{}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Msg.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(list.Msg.Items))
	}
	it := list.Msg.Items[0]
	if it.Price != 0 {
		t.Errorf("no-quote price want 0 (NULL), got %v", it.Price)
	}
	if it.PnlUsd != 0 {
		t.Errorf("no-quote pnl want 0 (NULL), got %v", it.PnlUsd)
	}
	// income_usd 无价按 0 → energy×0 + voting×0 + interest
	if it.IncomeUsd != 0 {
		t.Errorf("no-quote income_usd want 0, got %v", it.IncomeUsd)
	}
}

// TestListDailyIncome_RangeDescLimit：from/to 区间 + day 倒序 + limit 透传。
func TestListDailyIncome_RangeDescLimit(t *testing.T) {
	s := testServiceStore(t)
	ctx := context.Background()
	svc := &Service{store: s, qbus: bus.New(), freshTTL: time.Minute, liqMMR: 0.005}

	for _, d := range []string{"2026-08-15", "2026-08-16", "2026-08-17"} {
		if _, err := svc.AddDailyIncome(ctx, connect.NewRequest(&v1.AddDailyIncomeRequest{Day: d, EnergyTrx: 1})); err != nil {
			t.Fatalf("seed %s: %v", d, err)
		}
	}

	// 全量 → day 倒序
	all, err := svc.ListDailyIncome(ctx, connect.NewRequest(&v1.ListDailyIncomeRequest{}))
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all.Msg.Items) != 3 {
		t.Fatalf("want 3, got %d", len(all.Msg.Items))
	}
	wantOrder := []string{"2026-08-17", "2026-08-16", "2026-08-15"}
	for i, w := range wantOrder {
		if all.Msg.Items[i].Day != w {
			t.Errorf("order[%d] want %s, got %s", i, w, all.Msg.Items[i].Day)
		}
	}

	// 区间 [2026-08-16, 2026-08-17] → 2 行
	rng, err := svc.ListDailyIncome(ctx, connect.NewRequest(&v1.ListDailyIncomeRequest{From: "2026-08-16", To: "2026-08-17"}))
	if err != nil {
		t.Fatalf("List range: %v", err)
	}
	if len(rng.Msg.Items) != 2 {
		t.Fatalf("range want 2, got %d", len(rng.Msg.Items))
	}

	// limit 1 → 最新 1 行（08-17）
	lim, err := svc.ListDailyIncome(ctx, connect.NewRequest(&v1.ListDailyIncomeRequest{Limit: 1}))
	if err != nil {
		t.Fatalf("List limit: %v", err)
	}
	if len(lim.Msg.Items) != 1 || lim.Msg.Items[0].Day != "2026-08-17" {
		t.Errorf("limit 1 want 08-17, got %+v", lim.Msg.Items)
	}
}

// TestListDailyIncome_InvalidRange：from/to 非法 → CodeInvalidArgument。
func TestListDailyIncome_InvalidRange(t *testing.T) {
	svc := &Service{}
	ctx := context.Background()
	for _, req := range []*v1.ListDailyIncomeRequest{
		{From: "bad"},
		{To: "2026-13-01"},
	} {
		if _, err := svc.ListDailyIncome(ctx, connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("req %+v want CodeInvalidArgument, got %v", req, err)
		}
	}
}
