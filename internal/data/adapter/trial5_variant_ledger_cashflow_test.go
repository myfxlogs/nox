package adapter

// D-342R1 锚：swapFree override 收窄为 (server,broker_symbol) 白名单（D-300 S0 修订①）。
// T1=端到端——Trial5 XAUUSD247m emit→swapledger.Run→cashflow −0.54±2%（pos 492 复现；
// 前缀级 override 时 canonical=0→zero_swap skip→0 行=红）。
// T2=Trial5 标准形 XAUUSD 仍免（白名单具名命中，canonical 发 0）。
// T3=MT5Real5 全免不回归（通配豁免——real 账户 D-300 S0 账单级豁免不变）。

import (
	"context"
	"math"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"
	"nox/internal/ledger/swapledger"
	"nox/internal/store/pgstore"

	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
)

// d342StubStore swapledger.Store 最小桩——LatestFacts 回吐 emit 产物，cashflow 记录供断言。
type d342StubStore struct {
	pos   pgstore.Position
	legs  []pgstore.PositionLeg
	facts []fact.Fact
	cfs   []*pgstore.Cashflow
}

func (s *d342StubStore) ListOpenPositionsByType(_ context.Context, _ []string) ([]pgstore.Position, error) {
	return []pgstore.Position{s.pos}, nil
}
func (s *d342StubStore) ListPositionLegs(_ context.Context, _ int64) ([]pgstore.PositionLeg, error) {
	return s.legs, nil
}
func (s *d342StubStore) HasCashflowForLegDay(_ context.Context, _ int64, _ int16, _ string, _ time.Time) (bool, error) {
	return false, nil
}
func (s *d342StubStore) InsertCashflowFull(_ context.Context, c *pgstore.Cashflow) (bool, error) {
	s.cfs = append(s.cfs, c)
	return true, nil
}
func (s *d342StubStore) LatestFactsByKind(ctx context.Context, kind string) ([]fact.Fact, error) {
	return s.LatestFacts(ctx, kind, "", "")
}
func (s *d342StubStore) LatestFactsByKindSince(ctx context.Context, kind string, since time.Time) ([]fact.Fact, error) {
	return s.LatestFacts(ctx, kind, "", "")
}

func (s *d342StubStore) LatestFacts(_ context.Context, kind, _, _ string) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, f := range s.facts {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out, nil
}
func (s *d342StubStore) InsertAlert(_ context.Context, _ *int64, _, _ string) error { return nil }
func (s *d342StubStore) GetOpportunity(_ context.Context, _ string) (*pgstore.Opportunity, error) {
	return nil, nil
}
func (s *d342StubStore) ListSymbolMappings(_ context.Context, _ string) ([]pgstore.SymbolMapping, error) {
	return nil, nil
}

type d342Notional struct{ usdPerLot float64 }

func (n d342Notional) USDPerLotForLeg(_, _, _ string) (float64, bool) { return n.usdPerLot, true }

type d342Triple struct{}

func (d342Triple) TripleSwapDay(_, _ string) (time.Weekday, bool) { return time.Sunday, true }

// TestD342R1_Trial5VariantLedgerCashflow T1：pos 492 复现——Trial5 XAUUSD247m 腿。
// SwapLong=-4.5（PercCurPrice，同 XAUUSDm 族）→ bps/day=−1.2329 → 名义 $4.4k×0.01手
// → 日 swap −0.5425，与 venue 实收 −0.55 差 1.4%（±2% 内）。
func TestD342R1_Trial5VariantLedgerCashflow(t *testing.T) {
	a := &MT5Adapter{venue: "277842155", server: "Exness-MT5Trial5", mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		"XAUUSD247m": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_PercCurPrice, SwapLong: -4.5, SwapShort: 0,
			ThreeDaysSwap: mt5pb.V3DaysSwap_V3DaysSwap_Sunday,
			TradeMode:     mt5pb.TradeMode_TradeMode_FullAccess,
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.01, ContractSize: 100}}},
	}}}
	a.SetSymbolMap(map[string]string{"XAUUSD247m": "XAUUSD"})

	fs, err := a.SwapFacts(context.Background(), bus.New())
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	// canonical 应携真值（收窄后 variant 走正常值路径）；native 双发同值
	want := -4.5 / 100 / 365 * 10000 // −1.2329 bps/day
	if cv, ok := d325Value(fs, fact.KindSwapLong, "XAUUSD"); !ok || math.Abs(cv-want) > 1e-9 {
		t.Fatalf("canonical swap_long = %.4f ok=%v, want %.4f（Trial5 variant 真值入 canonical）", cv, ok, want)
	}
	if nv, ok := d325Value(fs, fact.KindSwapLong, "XAUUSD247m"); !ok || math.Abs(nv-want) > 1e-9 {
		t.Fatalf("native swap_long = %.4f ok=%v, want %.4f", nv, ok, want)
	}

	store := &d342StubStore{
		pos: pgstore.Position{ID: 492, Type: "XASSET_CARRY",
			OpportunityID: "XASSET_CARRY:XAUUSD:277842155:mt5:binance:perp"},
		legs: []pgstore.PositionLeg{
			{LegNo: 0, Phase: "OPEN", Venue: "277842155:mt5", Side: "BUY", Symbol: "XAUUSD247m", Qty: 0.01},
		},
		facts: fs,
	}
	l := swapledger.New(store, d342Notional{440000}, d342Triple{}, nil, 2*time.Hour)
	n, err := l.Run(context.Background())
	if err != nil {
		t.Fatalf("ledger Run: %v", err)
	}
	if n != 1 || len(store.cfs) != 1 {
		t.Fatalf("cashflow 行数 = %d（inserted %d），want 1（现 zero_swap skip）", n, len(store.cfs))
	}
	cf := store.cfs[0]
	got := cf.AmountUSD // −1.2329bps/1e4 × $4400 = −0.5425，实收 −0.55 差 1.4%
	if math.Abs(got-(-0.55)) > 0.55*0.02 {
		t.Errorf("cashflow amount = %.4f, want −0.55±2%%（venue 实收对账）", got)
	}
	if cf.Kind != "swap" || cf.Venue != "277842155:mt5" {
		t.Errorf("cashflow kind=%q venue=%q, want swap/277842155:mt5", cf.Kind, cf.Venue)
	}
}

// TestD342R1_Trial5StandardExempt T2：Trial5 标准形 XAUUSD 仍免——白名单具名命中发 0。
func TestD342R1_Trial5StandardExempt(t *testing.T) {
	a := &MT5Adapter{venue: "277842155", server: "Exness-MT5Trial5", mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		"XAUUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_PercCurPrice, SwapLong: -4.5, SwapShort: -1.0,
			TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.01, ContractSize: 100}}},
	}}}
	a.SetSymbolMap(map[string]string{"XAUUSD": "XAUUSD"})

	fs, err := a.SwapFacts(context.Background(), bus.New())
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	if cv, ok := d325Value(fs, fact.KindSwapLong, "XAUUSD"); !ok || cv != 0 {
		t.Errorf("canonical swap_long = %.4f ok=%v, want 0（Trial5 标准形白名单具名免）", cv, ok)
	}
}

// TestD342R1_RealVenueUnchanged T3：MT5Real5（59892915）通配豁免不回归——variant canonical 仍 0。
// （MT5Real/3001157 由既有 TestD325_SwapFacts_ExnessNativeRaw 锚定。）
func TestD342R1_RealVenueUnchanged(t *testing.T) {
	a := &MT5Adapter{venue: "59892915", server: "Exness-MT5Real5", mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		"XAUUSDm": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_PercCurPrice, SwapLong: -534.9, SwapShort: 13.0,
			TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.001, ContractSize: 100}}},
	}}}
	a.SetSymbolMap(map[string]string{"XAUUSDm": "XAUUSD"})

	fs, err := a.SwapFacts(context.Background(), bus.New())
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	if cv, ok := d325Value(fs, fact.KindSwapLong, "XAUUSD"); !ok || cv != 0 {
		t.Errorf("canonical swap_long = %.4f ok=%v, want 0（real 账户 D-300 S0 通配豁免不变）", cv, ok)
	}
}
