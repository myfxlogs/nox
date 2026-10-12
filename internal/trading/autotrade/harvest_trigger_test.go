package autotrade

// 收割出口（monitor floor 同行读，不受 minHold）+止损判距外化 max(4,noise) 两分支。
import (
	"context"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/risk/gate"
	"arb/internal/store/pgstore"
)

func d341strptr(s string) *string { return &s }

func d341fptr(f float64) *float64 { return &f }

// TestD341_HarvestTrigger D-014 R1 重写：收割判据=econ.NetIfClosedNow≥0 ∧ ≥CarryPerDay×7
// （floor 列停消费）。账底全量种子（d014EconPos：funding 20.8%/yr=$2.37/天，费 $2.08+
// 退出估 ~$2.6）+ basis=+40bps → capture≈$21.8 → net≈$17.1≥16.6 → harvest；
// 费率源缺 → 不收割（fail-closed）。
func TestD341_HarvestTrigger(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d014EconPos(t, s, 20.8)
	if _, err := s.Pool().Exec(ctx, `UPDATE positions SET expected_entry_spread_bps=-12.39 WHERE id=$1`, p.ID); err != nil {
		t.Fatalf("set anchor: %v", err)
	}
	rows, _ := s.ListPositions(ctx, "OPEN", 10, 0)
	p = &rows[0]
	e := &Engine{store: s, service: d014StubSvc{}, quotes: d404Quotes(time.Now().UTC(), 4016.0, 4000, 0),
		fundingBreach: map[int64]int{}, cfg: gate.Config{SwapFactMaxAge: 2 * time.Hour, PerpFundMinHold: 24 * time.Hour,
			XAssetQuoteMaxAge: 5 * time.Minute, EconFeeBps: d014FeeFn(true), HarvestHorizonDays: 7.0}}
	close, reason := e.checkExitCondition(ctx, p)
	if !close || !containsSub(reason, "harvest") {
		t.Errorf("net≥carry×7 应 harvest 出口, close=%v reason=%q", close, reason)
	}

	// 费率源缺 → harvestCheck hold_econ 不收割（其余退出闸同判于链上，本轮断言 harvest 链不触发）
	s2 := testAccumStore(t)
	d311Truncate(t, s2)
	p2 := d014EconPos(t, s2, 20.8)
	e2 := &Engine{store: s2, service: d014StubSvc{}, quotes: d404Quotes(time.Now().UTC(), 4016.0, 4000, 0),
		fundingBreach: map[int64]int{}, cfg: gate.Config{SwapFactMaxAge: 2 * time.Hour, PerpFundMinHold: 24 * time.Hour,
			XAssetQuoteMaxAge: 5 * time.Minute, EconFeeBps: d014FeeFn(false), HarvestHorizonDays: 7.0}}
	if close2, reason2 := e2.harvestCheck(ctx, p2); close2 {
		t.Errorf("费率源缺不应 harvest, reason=%q", reason2)
	}
}

func containsSub(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// TestD341_BasisStopNoiseFloor T3：止损判距=max(4, noise) 两分支（XASSET only）。
// entry=-14、current=-21 → pnl=-7：noise=10（max=10）→ -7>-10 不触发；noise=11（max=11）
// 仍不触发；恶化至 -26（pnl=-12）→ noise=10 触发。cfg.BasisStopBps=4 时 noise 主导。
func TestD341_BasisStopNoiseFloor(t *testing.T) {
	// 纯函数层：判距=max(cfg, noise) 语义
	mk := func(basisBps, stop float64) (bool, string) {
		return basisStopLossCheck(basisBps, true, d341fptr(-14), stop)
	}
	// stop=10：pnl=-7 不触发 / -12 触发
	if close, _ := mk(-21, 10); close {
		t.Error("noise=10：pnl=-7 不应触发（噪声内不止损）")
	}
	if close, _ := mk(-26, 10); !close {
		t.Error("pnl=-12 应触发（行为证 stop=10 生效）")
	}
	// stop=11：pnl=-11（-11>-11 边界不触发）/ -12 触发
	if close, _ := mk(-25, 11); close {
		t.Error("noise=11：pnl=-11 边界不触发")
	}
	if close, _ := mk(-26, 11); !close {
		t.Error("pnl=-12 应触发")
	}
	// 回归：无 noise（nil）→ cfg 常量语义不变（stop=4：pnl=-5 触发/-3 不触发）
	if close, _ := basisStopLossCheck(-19, true, d341fptr(-14), 4); !close {
		t.Error("stop=4：pnl=-5 应触发（回归）")
	}
	if close, _ := basisStopLossCheck(-17, true, d341fptr(-14), 4); close {
		t.Error("stop=4：pnl=-3 不应触发（回归）")
	}
}

// TestD341_BasisStopXAssetEngine T3 引擎层锚：basisStopCheckXAsset 判距外化
// （XASSET noise=10 → pnl=-7 不触发；noise=nil → cfg=4 → pnl=-4 触发）——M2 变异红点。
// basis 链=DSN legs+qbus 双腿报价（currentBasisBps 全真路径）。
func TestD341_BasisStopXAssetEngine(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	now := time.Now().UTC()
	oppID := "XASSET_CARRY:XPTUSD:3001157:mt5:okx:perp"
	if _, err := s.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: oppID, Type: "XASSET_CARRY", Canonical: "XPTUSD", CanonicalSell: d341strptr("XPTUSDT"),
		VenueBuy: "3001157:mt5", VenueSell: "okx:perp", ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed opp: %v", err)
	}
	posID, err := s.CreatePosition(ctx, &pgstore.Position{OpportunityID: oppID, Type: "XASSET_CARRY", Status: "OPEN", ExpectedEntrySpreadBps: d341fptr(-14)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	pa := 4000.0
	for _, l := range []pgstore.PositionLeg{
		{PositionID: posID, LegNo: 1, Phase: "OPEN", Venue: "3001157:mt5", Symbol: "XPTUSD", Side: "BUY", Qty: 1, Status: "FILLED", PriceActual: &pa},
		{PositionID: posID, LegNo: 2, Phase: "OPEN", Venue: "okx:perp", Symbol: "XPT-USDT-SWAP", Side: "SELL", Qty: 1, Status: "FILLED"},
	} {
		if err := s.CreatePositionLeg(ctx, &l); err != nil {
			t.Fatalf("create leg: %v", err)
		}
	}
	if _, err := s.Pool().Exec(ctx,
		`UPDATE positions SET expected_entry_spread_bps=-14 WHERE id=$1`, posID); err != nil {
		t.Fatalf("update entry spread: %v", err) // CreatePosition 不落该列（列外）
	}
	qb := bus.New()
	// basis=current(buy bid−sell ask)/ref×1e4：buy bid=3993/sell ask=4000, ref=PriceActual 4000
	// → current=-17.5bps；entry=-14 → pnl=-3.5（stop=4 内）；第二组 current=-26 → pnl=-12。
	qb.Publish(bus.Quote{Symbol: "XPTUSD", Venue: "3001157", VenueType: "mt5", Bid: 3993, Ask: 3994, Time: now, VenueSymbol: "XPTUSD"})
	qb.Publish(bus.Quote{Symbol: "XPTUSDT", Venue: "okx", VenueType: "perp", Bid: 3999, Ask: 4000, Time: now, VenueSymbol: "XPT-USDT-SWAP"})
	// kill 判据=CEX 腿 vs 跨所共识——共识源=同品种 XPTUSDT gate/binance perp
	// 贴腿 mid 3999.5；首轮 eval 锚定 (腿mid−共识中位) 吸收常驻价差。
	qb.Publish(bus.Quote{Symbol: "XPTUSDT", Venue: "gate", VenueType: "perp", Bid: 3999, Ask: 4000, Time: now, VenueSymbol: "XPT_USDT"})
	qb.Publish(bus.Quote{Symbol: "XPTUSDT", Venue: "binance", VenueType: "perp", Bid: 3999, Ask: 4000, Time: now, VenueSymbol: "XPTUSDT"})
	pOK, err := s.GetPosition(ctx, posID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// 窄阈 basisStopCheckXAsset 撤除，判距语义 1:1 迁移灾难带
	// （band=max(4,1×noise)、debounce=1 单次即杀——与旧 max(cfg,noise) 外化等价形态）。
	// 再改义：判距对象=CEX 腿锚后偏移（MT 侧偏离只告警，不再杀）。
	e1 := &Engine{store: s, quotes: qb, cfg: gate.Config{XAssetDisasterBps: 4, XAssetDisasterNoiseMult: 1, XAssetDisasterDebounce: 1, XAssetQuoteMaxAge: 5 * time.Minute}}
	p1 := *pOK
	p1.EntryNoiseBps = d341fptr(10)
	if close, reason, _, _ := e1.disasterCheckXAsset(ctx, &p1); close {
		t.Errorf("noise=10：锚定轮不应触发, reason=%q", reason)
	}
	// noise=nil：退 cfg=4 → 锚定后零偏移仍不触发（边界验证）
	p2 := *pOK
	if close, _, _, _ := e1.disasterCheckXAsset(ctx, &p2); close {
		t.Error("noise=nil cfg=4：零偏移不应触发（边界）")
	}
	// 恶化：okx perp mid 3999.5→4002.1 → 锚后偏移 6.5bps（noise=10 带内 / cfg=4 带外）
	qb.Publish(bus.Quote{Symbol: "XPTUSDT", Venue: "okx", VenueType: "perp", Bid: 4001.6, Ask: 4002.6, Time: now, VenueSymbol: "XPT-USDT-SWAP"})
	p3 := *pOK
	p3.EntryNoiseBps = d341fptr(10)
	if close, _, _, _ := e1.disasterCheckXAsset(ctx, &p3); close {
		t.Error("noise=10：cex dev=6.5 噪声内不应触发（M2 变异锚点同义迁移）")
	}
	p4 := *pOK
	p4.EntryNoiseBps = nil
	if close, reason, _, _ := e1.disasterCheckXAsset(ctx, &p4); !close {
		t.Errorf("noise=nil：cex dev=6.5 超 cfg=4 应触发, reason=%q", reason)
	}
}
