package dashboard

// D-340 T1-T4：持仓年化对照列（承诺→当前/已实现）。
// T1 装配锚按 R2 标准：红在「摘注入」（未注入 SetXAssetCarryLive → live 缺省），
// 非函数直连；T4=TTL 缓存两次调用一次引擎。
import (
	"context"
	"errors"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"
	"nox/internal/store/pgstore"
)

func d340strptr(s string) *string { return &s }

func d340Setup(t *testing.T) (*Service, *pgstore.Store, context.Context) {
	t.Helper()
	s := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs, opportunities CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	svc := &Service{store: s, qbus: bus.New()}
	return svc, s, ctx
}

// d340SeedXPT D-311 锚⑧a 同款 fixture：买 MT5（swap_long 2bps/日）+卖 okx perp（funding −10%年化）
// → recalc netCarry=2×3.65−10=−2.7%（手算锚现成）。
func d340SeedXPT(t *testing.T, s *pgstore.Store, status string, realized *float64, factAge time.Duration) *pgstore.Position {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	oppID := "XASSET_CARRY:XPTUSD:3001157:mt5:okx:perp"
	if _, err := s.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: oppID, Type: "XASSET_CARRY", Canonical: "XPTUSD", CanonicalSell: d340strptr("XPTUSDT"),
		VenueBuy: "3001157:mt5", VenueSell: "okx:perp", ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed opp: %v", err)
	}
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "3001157", Symbol: "XPTUSD", Value: 2.0, Unit: fact.UnitBpsDaily, Ts: now.Add(-factAge), Src: "t"},
		{Kind: fact.KindFunding, Venue: "okx", Symbol: "XPTUSDT", Value: -10.0, Unit: fact.UnitPctAnnualized, Ts: now.Add(-factAge), Src: "t"},
	}); err != nil {
		t.Fatalf("seed facts: %v", err)
	}
	posID, err := s.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: oppID, Type: "XASSET_CARRY", Status: status,
	})
	if err != nil {
		t.Fatalf("create pos: %v", err)
	}
	// CreatePosition 不落 realized/closed（列外）——补 UPDATE（hold=29 天锚）。
	if _, err := s.Pool().Exec(ctx,
		`UPDATE positions SET opened_at=$1, closed_at=$2, realized_pnl=$3 WHERE id=$4`,
		now.Add(-30*24*time.Hour), now.Add(-24*time.Hour), realized, posID); err != nil {
		t.Fatalf("update pos: %v", err)
	}
	for _, l := range []pgstore.PositionLeg{
		{PositionID: posID, LegNo: 1, Phase: "OPEN", Venue: "3001157:mt5", Symbol: "XPTUSD", Side: "BUY", Qty: 1.0, Status: "FILLED"},
		{PositionID: posID, LegNo: 2, Phase: "OPEN", Venue: "okx:perp", Symbol: "XPT-USDT-SWAP", Side: "SELL", Qty: 1.0, Status: "FILLED"},
	} {
		if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
			PositionID: posID, LegNo: l.LegNo, Phase: l.Phase, Venue: l.Venue, Symbol: l.Symbol,
			Side: l.Side, Qty: l.Qty, Status: l.Status,
		}); err != nil {
			t.Fatalf("create leg: %v", err)
		}
	}
	p, err := s.GetPosition(ctx, posID)
	if err != nil {
		t.Fatalf("get pos: %v", err)
	}
	return p
}

func ptrTime(t time.Time) *time.Time { return &t }

// TestD340_LiveNetCarryInjected T1：注入引擎公开包装 → OPEN XASSET 行 live=−2.7。
// 注入 stub 返回 −2.7=recalcXAssetNetCarry 手算值（2×3.65−10）——该数值锚由 autotrade 包
// D-311 锚⑧a 承担（import 环：dashboard 测试不能引 autotrade；P3 分工不复制）。
// 对照锚（R2 标准）：未注入 Service 同仓 live 缺省 nil（红=摘 setup 注入行的生产形态）。
func TestD340_LiveNetCarryInjected(t *testing.T) {
	svc, s, ctx := d340Setup(t)
	p := d340SeedXPT(t, s, "OPEN", nil, 0)
	svc.SetXAssetCarryLive(func(context.Context, int64) (float64, bool) { return -2.7, true })
	pb := svc.positionToProto(ctx, p)
	if pb.LiveNetCarryPct == nil {
		t.Fatalf("live 应有值（红=摘注入/现无字段）")
	}
	if absF340(*pb.LiveNetCarryPct-(-2.7)) > 1e-9 {
		t.Errorf("live want −2.7（手算 2×3.65−10）, got %v", *pb.LiveNetCarryPct)
	}
	// 对照：未注入 → 缺省（fail-closed）
	svc2 := &Service{store: s, qbus: bus.New()}
	pb2 := svc2.positionToProto(ctx, p)
	if pb2.LiveNetCarryPct != nil {
		t.Errorf("未注入 live 应缺省 nil, got %v", *pb2.LiveNetCarryPct)
	}
}

// TestD340_LiveStaleFailClosed T2：facts 过期（>SwapFactMaxAge）→ live 缺省（与退出判定同病同药）。
func TestD340_LiveStaleFailClosed(t *testing.T) {
	svc, s, ctx := d340Setup(t)
	p := d340SeedXPT(t, s, "OPEN", nil, 3*time.Hour) // facts 3h 前 > 2h maxAge
	svc.SetXAssetCarryLive(func(context.Context, int64) (float64, bool) { return 0, false })
	pb := svc.positionToProto(ctx, p)
	if pb.LiveNetCarryPct != nil {
		t.Errorf("facts 过期 live 应缺省（fail-closed 同引擎）, got %v", *pb.LiveNetCarryPct)
	}
}

// TestD340_RealizedAnnual T3：CLOSED+realized → realized_annual=realized/(Σqty×usdPerLot)×hold×365×100 折算锚。
func TestD340_RealizedAnnual(t *testing.T) {
	svc, s, ctx := d340Setup(t)
	r := 100.0
	p := d340SeedXPT(t, s, "CLOSED", &r, 0)
	// 双腿 qty 各 1.0；usdPerLot：qbus 报价 XPTUSD@3001157:mt5 ask=4000（CS=1 stub）+XPTUSDT@okx:perp bid=4000
	// → 名义 Σ=4000×1+4000×1=8000；hold=（closed−opened)=29 天 → annual=100/8000×365/29×100=157.3%
	now := time.Now()
	svc.qbus.Publish(bus.Quote{Symbol: "XPTUSD", Venue: "3001157", VenueType: "mt5", Bid: 3999, Ask: 4000, Time: now, VenueSymbol: "XPTUSD"})
	svc.qbus.Publish(bus.Quote{Symbol: "XPTUSDT", Venue: "okx", VenueType: "perp", Bid: 4000, Ask: 4001, Time: now, VenueSymbol: "XPT-USDT-SWAP"})
	svc.SetLegContractSize(func(_, _ string) (float64, bool) { return 1, true })
	svc.SetCexContractSize(func(_, _ string) (float64, bool) { return 1, true }) // okx:perp 腿 cexContractSize 路径
	pb := svc.positionToProto(ctx, p)
	if pb.RealizedAnnualPct == nil {
		t.Fatalf("realized_annual 应有值（红=现无字段/摘公式）")
	}
	notional := 4000.0 + 4000.0
	hold := 29.0
	want := r / notional * 365 / hold * 100
	if absF340(*pb.RealizedAnnualPct-want) > 0.01 {
		t.Errorf("realized_annual want %.4f, got %.4f", want, *pb.RealizedAnnualPct)
	}
}

// TestD340_TTLCacheOnce T4：TTL 缓存——两次 positionToProto 同仓 → 引擎包装仅被调一次。
func TestD340_TTLCacheOnce(t *testing.T) {
	svc, s, ctx := d340Setup(t)
	p := d340SeedXPT(t, s, "OPEN", nil, 0)
	calls := 0
	svc.SetXAssetCarryLive(func(context.Context, int64) (float64, bool) {
		calls++
		return -2.7, true
	})
	_ = svc.positionToProto(ctx, p)
	_ = svc.positionToProto(ctx, p)
	if calls != 1 {
		t.Errorf("TTL 15s 内两次渲染应一次引擎（红=摘 TTL）, got %d", calls)
	}
	// 装配非 nil 语义（R2 标准锚的注入面）：未注入由 T1 对照例覆盖
	_ = errors.New
}

func absF340(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
