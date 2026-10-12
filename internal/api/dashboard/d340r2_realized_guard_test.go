package dashboard

// D-340R2 T：realized 年化短仓护栏——hold_days<1 → realized_annual_pct 缺省（不足一日
// 不折年化，绝对盈亏在既有 realized 字段可见——诚实空态优于荒谬值）；hold≥1 正常折算。
// 红=现公式 hold→0 时年化爆炸（491 实证 −6417%）。fixture 复用 D-340 d340SeedXPT，
// 双腿报价+CS stub 全配使名义可得——变异锚：摘 guard 现公式出非缺省值。
import (
	"context"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/store/pgstore"
)

// d340r2Quotes 双腿报价+CS stub（与 T3 同形）：buy XPTUSD@3001157 ask=4000 +
// sell XPTUSDT@okx bid=4000 → 名义 Σ=8000。
func d340r2Quotes(svc *Service) {
	now := time.Now()
	svc.qbus.Publish(bus.Quote{Symbol: "XPTUSD", Venue: "3001157", VenueType: "mt5", Bid: 3999, Ask: 4000, Time: now, VenueSymbol: "XPTUSD"})
	svc.qbus.Publish(bus.Quote{Symbol: "XPTUSDT", Venue: "okx", VenueType: "perp", Bid: 4000, Ask: 4001, Time: now, VenueSymbol: "XPT-USDT-SWAP"})
	svc.SetLegContractSize(func(_, _ string) (float64, bool) { return 1, true })
	svc.SetCexContractSize(func(_, _ string) (float64, bool) { return 1, true }) // okx:perp 腿 cexContractSize 路径
}

// d340r2Refetch UPDATE 后重取 position（fixture 返回的是 UPDATE 前快照）。
func d340r2Refetch(t *testing.T, s *pgstore.Store, ctx context.Context, posID int64) *pgstore.Position {
	t.Helper()
	p, err := s.GetPosition(ctx, posID)
	if err != nil {
		t.Fatalf("refetch pos: %v", err)
	}
	return p
}

func TestD340R2_RealizedShortHoldGuard(t *testing.T) {
	svc, s, ctx := d340Setup(t)
	r := -2.46
	p := d340SeedXPT(t, s, "CLOSED", &r, 0)
	if _, err := s.Pool().Exec(ctx,
		`UPDATE positions SET opened_at=now()-interval '12 hours', closed_at=now() WHERE id=$1`, p.ID); err != nil {
		t.Fatalf("set hold 0.5d: %v", err)
	}
	p = d340r2Refetch(t, s, ctx, p.ID)
	d340r2Quotes(svc) // 名义可得——摘 guard 现公式出 −22.4%（红=非缺省）
	pb := svc.positionToProto(ctx, p)
	if pb.RealizedAnnualPct != nil {
		t.Errorf("hold=0.5 天应缺省（不足一日不折年化）, got %v（红=现公式爆炸值）", *pb.RealizedAnnualPct)
	}
}

// TestD340R2_RealizedLongHoldNormal hold=2 天 → 正常折算（护栏不误伤长仓）。
func TestD340R2_RealizedLongHoldNormal(t *testing.T) {
	svc, s, ctx := d340Setup(t)
	r := -100.0
	p := d340SeedXPT(t, s, "CLOSED", &r, 0)
	if _, err := s.Pool().Exec(ctx,
		`UPDATE positions SET opened_at=now()-interval '30 days', closed_at=now()-interval '28 days' WHERE id=$1`, p.ID); err != nil {
		t.Fatalf("set hold 2d: %v", err)
	}
	p = d340r2Refetch(t, s, ctx, p.ID)
	d340r2Quotes(svc)
	pb := svc.positionToProto(ctx, p)
	if pb.RealizedAnnualPct == nil {
		t.Fatal("hold=2 天长仓应正常折算（护栏不误伤）")
	}
	// 名义=双腿 Σ(qty×usdPerLot)：buy 腿 ask=4000 + sell 腿 bid=4000 → 8000
	// realized=-100、hold=2 → -100/8000×365/2×100=-228.125
	want := -100.0 / 8000 * 365 / 2 * 100
	if absF340(*pb.RealizedAnnualPct-want) > 0.01 {
		t.Errorf("realized_annual want %.2f, got %.4f", want, *pb.RealizedAnnualPct)
	}
}
