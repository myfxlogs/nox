package dashboard

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"connectrpc.com/connect"
)

func f64(v float64) *float64 { return &v }
func i32(v int) *int         { return &v }

// TestPnlAndLiq：pnl=(entry−price)×qty；liq 理论值 + crit_high 覆盖优先 + MMR 参数化 + 距强平%（含已破线负数）。
func TestPnlAndLiq(t *testing.T) {
	now := time.Unix(1000, 0)
	venues := func(price float64) map[string]bus.Quote {
		return map[string]bus.Quote{
			"binance:spot": {Symbol: "TRXUSDT", Venue: "binance", VenueType: "spot", Bid: price, Ask: price, Time: now},
		}
	}
	short := &pgstore.PersonalPosition{
		ID: 1, Asset: "TRX", Kind: "short_hedge", PriceSymbol: "TRXUSDT",
		Qty: f64(0), EntryPrice: f64(0), Leverage: i32(10), Status: "OPEN",
	}

	t.Run("pnl=(entry-price)*qty", func(t *testing.T) {
		pb := personalPositionToProto(short, venues(0.33), time.Minute, now, 0, 0.005)
		want := (0 - 0.33) * 0 // 30.951
		if math.Abs(pb.PnlUsd-want) > 1e-9 {
			t.Errorf("pnl want %v, got %v", want, pb.PnlUsd)
		}
	})

	t.Run("liq crit_high overrides theoretical", func(t *testing.T) {
		crit := 0.3695
		p := *short
		p.CritHigh = &crit
		pb := personalPositionToProto(&p, venues(0.33), time.Minute, now, 0, 0.005)
		if pb.LiqPrice != 0.3695 {
			t.Errorf("liq want crit_high 0.3695, got %v", pb.LiqPrice)
		}
		// 距强平 = (0.3695−0.33)/0.33
		want := (0.3695 - 0.33) / 0.33
		if math.Abs(pb.DistToLiqPct-want) > 1e-9 {
			t.Errorf("dist want %v, got %v", want, pb.DistToLiqPct)
		}
	})

	t.Run("liq theoretical + MMR param", func(t *testing.T) {
		// crit_high 未设 → 理论 = entry × (1 + 1/leverage − MMR)
		pb := personalPositionToProto(short, venues(0.33), time.Minute, now, 0, 0.005)
		want := 0 * (1 + 1.0/10 - 0.005)
		if math.Abs(pb.LiqPrice-want) > 1e-9 {
			t.Errorf("liq theoretical want %v, got %v", want, pb.LiqPrice)
		}
		// MMR 参数化：更高 MMR → 更低强平价
		pb2 := personalPositionToProto(short, venues(0.33), time.Minute, now, 0, 0.01)
		want2 := 0 * (1 + 1.0/10 - 0.01)
		if math.Abs(pb2.LiqPrice-want2) > 1e-9 {
			t.Errorf("liq theoretical MMR=0.01 want %v, got %v", want2, pb2.LiqPrice)
		}
		if pb2.LiqPrice >= pb.LiqPrice {
			t.Errorf("higher MMR should lower liq: %v >= %v", pb2.LiqPrice, pb.LiqPrice)
		}
	})

	t.Run("dist negative when broken line", func(t *testing.T) {
		crit := 0.3695
		p := *short
		p.CritHigh = &crit
		// price 0.37 > liq 0.3695 → 已破线，负数
		pb := personalPositionToProto(&p, venues(0.37), time.Minute, now, 0, 0.005)
		want := (0.3695 - 0.37) / 0.37
		if pb.DistToLiqPct >= 0 || math.Abs(pb.DistToLiqPct-want) > 1e-9 {
			t.Errorf("dist want negative %v, got %v", want, pb.DistToLiqPct)
		}
	})

	t.Run("staking no entry -> pnl=0 liq=0", func(t *testing.T) {
		staking := &pgstore.PersonalPosition{
			ID: 2, Asset: "TRX", Kind: "staking", PriceSymbol: "TRXUSDT",
			Qty: f64(0), Status: "OPEN",
		}
		pb := personalPositionToProto(staking, venues(0.33), time.Minute, now, 0, 0.005)
		if pb.PnlUsd != 0 {
			t.Errorf("staking pnl want 0, got %v", pb.PnlUsd)
		}
		if pb.LiqPrice != 0 || pb.DistToLiqPct != 0 {
			t.Errorf("staking liq/dist want 0/0, got %v/%v", pb.LiqPrice, pb.DistToLiqPct)
		}
	})

	t.Run("no quote -> pnl=0 liq theoretical still set", func(t *testing.T) {
		// 无报价 → price=0、pnl=0（无价不算）；liq 理论值仍可由 entry/leverage 算。
		pb := personalPositionToProto(short, nil, time.Minute, now, 0, 0.005)
		if pb.Price != 0 || pb.PnlUsd != 0 {
			t.Errorf("no quote want price=0 pnl=0, got %v/%v", pb.Price, pb.PnlUsd)
		}
		if pb.LiqPrice == 0 {
			t.Errorf("no quote liq theoretical should still be set, got 0")
		}
	})
}

// TestListPersonalPositions_RealFields：真库种子（migration 0008 回填）→ qty/entry/leverage/opened_at 透传 +
// pnl/liq/dist 组装 + accrued_interest 累计；staking 腿（无 entry）→ pnl=0/liq=0。
func TestListPersonalPositions_RealFields(t *testing.T) {
	s := testServiceStore(t)
	ctx := context.Background()

	// 经 AddDailyIncome 写入两条利息记录（累计利息 = 0 + 0.5）
	day1 := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	if _, err := s.AddDailyIncome(ctx, day1, 0, 0, 0, nil, nil, ""); err != nil {
		t.Fatalf("seed income day1: %v", err)
	}
	if _, err := s.AddDailyIncome(ctx, day2, 0, 0, 0.5, nil, nil, ""); err != nil {
		t.Fatalf("seed income day2: %v", err)
	}

	// 注入实时 TRXUSDT 报价（新鲜帧）
	qbus := bus.New()
	price := 0.33
	now := time.Now()
	qbus.Publish(bus.Quote{Symbol: "TRXUSDT", Venue: "binance", VenueType: "spot", Bid: price, Ask: price, Time: now})

	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.ListPersonalPositions(ctx, connect.NewRequest(&v1.ListPersonalPositionsRequest{}))
	if err != nil {
		t.Fatalf("ListPersonalPositions: %v", err)
	}
	if len(resp.Msg.Items) != 2 {
		t.Fatalf("want 2 seed positions, got %d", len(resp.Msg.Items))
	}

	var short, staking *v1.PersonalPosition
	for _, it := range resp.Msg.Items {
		switch it.Kind {
		case "short_hedge":
			short = it
		case "staking":
			staking = it
		}
	}
	if short == nil || staking == nil {
		t.Fatalf("want both short_hedge and staking rows, got %d items", len(resp.Msg.Items))
	}

	// short_hedge：回填字段透传
	if short.Qty != 0 || short.EntryPrice != 0 || short.Leverage != 10 {
		t.Errorf("short passthrough want qty=0 entry=0 lev=10, got %+v", short)
	}
	if short.OpenedAt == nil {
		t.Errorf("short opened_at want set, got nil")
	} else if short.OpenedAt.AsTime().Format("2006-01-02") != "2026-08-17" {
		t.Errorf("short opened_at want 2026-08-17, got %v", short.OpenedAt.AsTime())
	}

	// short_hedge：price/pnl/liq/dist 组装（crit_high=0.3695 覆盖理论值）
	if short.Price != 0.33 {
		t.Errorf("short price want 0.33, got %v", short.Price)
	}
	wantPnl := (0 - 0.33) * 0
	if math.Abs(short.PnlUsd-wantPnl) > 1e-9 {
		t.Errorf("short pnl want %v, got %v", wantPnl, short.PnlUsd)
	}
	if short.LiqPrice != 0.3695 {
		t.Errorf("short liq want crit_high 0.3695, got %v", short.LiqPrice)
	}
	wantDist := (0.3695 - 0.33) / 0.33
	if math.Abs(short.DistToLiqPct-wantDist) > 1e-9 {
		t.Errorf("short dist want %v, got %v", wantDist, short.DistToLiqPct)
	}
	// accrued_interest 累计（两条记录求和）
	if math.Abs(short.AccruedInterestUsd-0) > 1e-9 {
		t.Errorf("short accrued want 0, got %v", short.AccruedInterestUsd)
	}

	// staking：无 entry → pnl=0/liq=0；qty 透传
	if staking.Qty != 0 {
		t.Errorf("staking qty want 0, got %v", staking.Qty)
	}
	if staking.EntryPrice != 0 || staking.Leverage != 0 || staking.OpenedAt != nil {
		t.Errorf("staking entry/lev/opened want unset (0/0/nil), got %+v", staking)
	}
	if staking.PnlUsd != 0 || staking.LiqPrice != 0 || staking.DistToLiqPct != 0 {
		t.Errorf("staking pnl/liq/dist want 0/0/0, got %v/%v/%v", staking.PnlUsd, staking.LiqPrice, staking.DistToLiqPct)
	}
}
