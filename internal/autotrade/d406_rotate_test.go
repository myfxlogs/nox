package autotrade

// D-406 S5：轮转比较器测试矩阵——触发式/日限/对冷却/资金前置 fail-closed/前闸短路。
// 口径钉值（SSOT §1）：curEV=curCarry+剩余收敛年化；gain30d=(altEV−curEV)/100×notional×30/365；
// 触发条件 gain30d > switchCost×2。
import (
	"context"
	"fmt"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/dashboard"
	"arb/internal/exec"
	"arb/internal/fact"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
)

const (
	d406CurOpp = "XASSET_CARRY:XPTUSD:3001157:mt5:okx:perp"
	d406AltOpp = "XASSET_CARRY:XAUUSD:3001158:mt5:binance:perp"
)

// d406Engine 轮转测试引擎：service 接真 store（MarginFloorKnown 数据源=AccountMonitor 实时态），
// CS resolver 注入（MT=100/CEX=1）；fundingBreach/disasterBreach 初始化。
func d406Engine(s *pgstore.Store, qb *bus.QuoteBus, maxPerDay int) *Engine {
	// D-549/3d-P1#1c：CEX 隐含率锚 buffer_multiple 改读 AccountMonitor 实时态（快照表
	// 退出决策面）——经 AddStatic 假查询器喂 binance:perp（equity 2000/buffer 4）。
	bm := dashboard.NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	acctMon := dashboard.NewAccountMonitor(bm, nil)
	acctMon.AddStatic(&mockAcctQuerier{key: "binance:perp", acct: &reconcile.AccountInfo{
		VenueKey: "binance:perp", Venue: "binance", VenueType: "perp", AssetType: "cex",
		Equity: 2000, Currency: "USD", BufferMultiple: 4.0,
	}})
	acctMon.Refresh(context.Background())
	svc := dashboard.New(s, qb, nil, nil, nil, 0, 0, 0, 0, 0, 0, nil, nil, nil, 0, acctMon, nil, 0, 0, nil, 0)
	svc.SetLegContractSize(func(_, _ string) (float64, bool) { return 100, true })
	svc.SetCexContractSize(func(_, _ string) (float64, bool) { return 1.0, true })
	// D-549：CEX 执行链资金数=活账本/REST 实拉（快照兜底禁用）——喂 ws 活值
	// （buffer_multiple 隐含率锚走上方 acctMon 假查询器；快照行仅 MT 腿净值/free_margin）。
	led := bus.NewBalanceLedger()
	svc.SetBalanceLedger(led)
	led.Put("binance:perp", 1500, 2000, "USD", "ws", time.Now())
	led.Put("okx:unified", 1500, 2000, "USD", "ws", time.Now())
	// D-563 S4b 涟漪：MT 腿判定资金=AccountSummary 实时 RPC（快照退决策面）——
	// 测试缝注入等价值（3001158=eq2000/fm1000 镜像旧快照；其他 venue=rpc 死
	// 即 withFunding=false 面）。
	svc.SetMTSummaryFunc(func(_ context.Context, vk string) (float64, float64, string, error) {
		if vk == "3001158:mt5" {
			return 2000, 1000, "USD", nil
		}
		return 0, 0, "", fmt.Errorf("rpc dead（测试缝：未种 venue=RPC 败）")
	})
	return &Engine{
		store: s, service: svc, quotes: qb,
		fundingBreach:  map[int64]int{},
		disasterBreach: map[int64]int{},
		cfg: Config{
			XAssetQuoteMaxAge:  5 * time.Minute,
			XAssetExecDriftBps: 100, // 测试聚焦轮转，漂移闸放宽不干扰
			SwapFactMaxAge:     time.Hour,
			RotateMaxPerDay:    maxPerDay,
			XAssetDisasterBps:  40,
			XAssetAllowCombos: []exec.XAssetComboAllowSpec{
				{CanonicalBuy: "XPTUSD", CanonicalSell: "XPTUSDT", VenueBuy: "3001157:mt5", VenueSell: "okx:perp"},
				{CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT", VenueBuy: "3001158:mt5", VenueSell: "binance:perp"},
			},
		},
	}
}

// d406Seed 全要素 fixture：当前仓（锚三要素+notional）+ monitor 行（ts≤opened_at）+
// 替代 opp（新鲜+白名单内）+ carry facts + 目标 venue 资金数据（快照+同 venue OPEN 腿
// 供隐含保证金率）。altFunding 控制 altEV−curEV 落差（触发式唯一旋钮）。
// withFunding=false → 不种快照/替代腿仓（资金前置 fail-closed 用例）。
func d406Seed(t *testing.T, s *pgstore.Store, altFunding float64, withFunding bool) *pgstore.Position {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	// 当前仓：复用 D-404 fixture（锚 floor=4.73/anchor=−12.39 后续 UPDATE），补 notional。
	p := d404XAssetPos(t, s, d341fptr(-12.39), d341fptr(4.73))
	if err := s.UpdatePositionNotional(ctx, p.ID, 4000); err != nil {
		t.Fatalf("set notional: %v", err)
	}
	// 双腿成交价（EntrySpreadBpsFromLegs 需双腿 price_actual 才 ok → 观测行不落
	// fail_closed:no_legs）；fill=(4000−4000)/4000=0 → drift=12.39<阈值 100 不触漂移闸。
	if _, err := s.Pool().Exec(ctx,
		`UPDATE position_legs SET price_actual=4000 WHERE position_id=$1 AND side='SELL'`, p.ID); err != nil {
		t.Fatalf("set sell fill: %v", err)
	}
	d404SeedMonitorRowAt(t, s, p.OpenedAt.Add(-5*time.Second), 7.73, 4.73)
	// 替代 opp：新鲜行（QuoteTime=now），白名单内组合。
	if _, err := s.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: d406AltOpp, Type: "XASSET_CARRY", Canonical: "XAUUSD", CanonicalSell: d311strptr("XAUUSDT"),
		VenueBuy: "3001158:mt5", VenueSell: "binance:perp",
		QuoteTime: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed alt opp: %v", err)
	}
	// carry facts：cur = 3001157 swap_long 5bps/日×3.65 + okx funding 0 = 18.25%/yr；
	// alt = 3001158 swap_long 5bps/日×3.65 + binance funding altFunding。
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "3001157", Symbol: "XPTUSD", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindFunding, Venue: "okx", Symbol: "XPTUSDT", Value: 0, Unit: fact.UnitPctAnnualized, Ts: now, Src: "okx"},
		{Kind: fact.KindSwapLong, Venue: "3001158", Symbol: "XAUUSD", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindFunding, Venue: "binance", Symbol: "XAUUSDT", Value: altFunding, Unit: fact.UnitPctAnnualized, Ts: now, Src: "binance"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	if !withFunding {
		// ListPositions 重取（UpdatePositionNotional 后 p.NotionalUSD 是旧值 nil）。
		return d406Relist(t, s, p.ID)
	}
	// 目标 venue 资金数据：快照 + 同 venue OPEN 腿（隐含保证金率源）。
	eq, free, buf := 2000.0, 1000.0, 4.0
	if err := s.InsertVenueAccountSnapshots(ctx, []pgstore.VenueAccountSnapshot{
		{Venue: "3001158", VenueType: "mt5", EquityRaw: eq, FreeMarginRaw: free, Currency: "USD", FetchedAt: now},
		{Venue: "binance", VenueType: "perp", EquityRaw: eq, BufferMultiple: &buf, Currency: "USD", FetchedAt: now},
	}); err != nil {
		t.Fatalf("seed snapshots: %v", err)
	}
	// 第二仓双腿落在替代 venue（marginFloorEval 隐含率=used/Σ该 venue OPEN 腿名义）。
	// opp 复用 alt 行——canonicalOfOppID 须与报价 canonical "XAUUSD" 一致才有名义可算。
	pos2, err := s.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: d406AltOpp, Type: "XASSET_CARRY", Status: "OPEN"})
	if err != nil {
		t.Fatalf("create pos2: %v", err)
	}
	pa := 4000.0
	for _, l := range []pgstore.PositionLeg{
		{PositionID: pos2, LegNo: 1, Phase: "OPEN", Venue: "3001158:mt5", Symbol: "XAUUSD", Side: "BUY", Qty: 1, Status: "FILLED", PriceActual: &pa},
		{PositionID: pos2, LegNo: 2, Phase: "OPEN", Venue: "binance:perp", Symbol: "XAU-USDT-SWAP", Side: "SELL", Qty: 1, Status: "FILLED", PriceActual: &pa},
	} {
		if err := s.CreatePositionLeg(ctx, &l); err != nil {
			t.Fatalf("create pos2 leg: %v", err)
		}
	}
	// ListPositions 取回（notional_usd/锚三列同生产 scanAndClose 口径）。
	return d406Relist(t, s, p.ID)
}

// d406Relist 按 id 从 ListPositions(OPEN) 取回（与生产 scanAndClose 同读口）。
func d406Relist(t *testing.T, s *pgstore.Store, posID int64) *pgstore.Position {
	t.Helper()
	rows, err := s.ListPositions(context.Background(), "OPEN", 50, 0)
	if err != nil {
		t.Fatalf("list pos: %v", err)
	}
	for i := range rows {
		if rows[i].ID == posID {
			return &rows[i]
		}
	}
	t.Fatalf("pos %d not in list", posID)
	return nil
}

// d406Quotes 双腿报价（basis=−11：未收敛过收割线 sign·basis=11>line 9.39、
// dev=|−11−(−12.39)|=1.39<band 40 不触灾难带）+ 替代 venue 腿名义报价（margin 隐含率输入）。
func d406Quotes(now time.Time, age time.Duration) *bus.QuoteBus {
	qb := bus.New()
	ts := now.Add(-age)
	qb.Publish(bus.Quote{Symbol: "XPTUSD", Venue: "3001157", VenueType: "mt5", Bid: 3995.6, Ask: 3996.6, Time: ts, VenueSymbol: "XPTUSD"})
	qb.Publish(bus.Quote{Symbol: "XPTUSDT", Venue: "okx", VenueType: "perp", Bid: 3999, Ask: 4000, Time: ts, VenueSymbol: "XPT-USDT-SWAP"})
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "3001158", VenueType: "mt5", Bid: 3999.5, Ask: 4000.5, Time: ts, VenueSymbol: "XAUUSD"})
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "binance", VenueType: "perp", Bid: 3999.5, Ask: 4000.5, Time: ts, VenueSymbol: "XAU-USDT-SWAP"})
	return qb
}

// d406LastEval 该仓最新观测行 (decision, detail)。
func d406LastEval(t *testing.T, s *pgstore.Store, posID int64) (string, string) {
	t.Helper()
	var dec, detail string
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT decision, COALESCE(detail,'') FROM position_evals WHERE position_id=$1 ORDER BY evaluated_at DESC LIMIT 1`,
		posID).Scan(&dec, &detail); err != nil {
		t.Fatalf("last eval: %v", err)
	}
	return dec, detail
}

// TestD406_RotateTrigger ⑤触发式：altEV−curEV 落差超 switchCost×2 → close + rotate_to:<alt>。
// 钉值：curCarry=18.25（MT 5bps/日×3.65 + okx 0），altEV=33.25（+binance funding 15），
// remBps=|−11|−9.39=1.61→curEV≈18.45；gain30d=14.8×4000×30/365/100≈48.7 >
// cost×2=((0.3+5)×2×4000/1e4+1)×2≈10.5。
func TestD406_RotateTrigger(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d406Seed(t, s, 15.0, true)
	e := d406Engine(s, d406Quotes(time.Now().UTC(), 0), 1)
	close, reason := e.xassetExitCheck(ctx, p)
	if !close || !containsSub(reason, "rotate_to:"+d406AltOpp) {
		t.Fatalf("落差超阈应触发轮转 close=%v reason=%q", close, reason)
	}
	dec, detail := d406LastEval(t, s, p.ID)
	if dec != "rotate" || !containsSub(detail, "alt="+d406AltOpp) {
		t.Errorf("观测行应落 decision=rotate 且 detail 含 alt=%s, got %s %q", d406AltOpp, dec, detail)
	}
}

// TestD406_RotateBelowThreshold ⑤落差不足 → 不轮转，观测行 hold_rotate_check 带三值。
// altFunding=1 → altEV=19.25，diff≈0.8 → gain30d≈2.6 < 10.5。
func TestD406_RotateBelowThreshold(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d406Seed(t, s, 1.0, true)
	e := d406Engine(s, d406Quotes(time.Now().UTC(), 0), 1)
	close, reason := e.xassetExitCheck(ctx, p)
	if close {
		t.Fatalf("落差不足不应轮转 reason=%q", reason)
	}
	dec, detail := d406LastEval(t, s, p.ID)
	if dec != "hold_rotate_check" || !containsSub(detail, "altEV=") || !containsSub(detail, "cost=") {
		t.Errorf("观测行应落 hold_rotate_check 带三值, got %s %q", dec, detail)
	}
}

// TestD406_RotateDailyLimit ④日限：24h 内已有 1 条 rotate 行 → 本评拦阻（maxPerDay=1）。
func TestD406_RotateDailyLimit(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d406Seed(t, s, 15.0, true)
	// 先种一条 rotate 观测行（另一仓当日已轮转过一次）。
	if err := s.InsertPositionEval(ctx, pgstore.PositionEval{
		PositionID: p.ID, Decision: "rotate", Detail: "alt=" + d406AltOpp,
	}); err != nil {
		t.Fatalf("seed rotate eval: %v", err)
	}
	e := d406Engine(s, d406Quotes(time.Now().UTC(), 0), 1)
	close, _ := e.xassetExitCheck(ctx, p)
	if close {
		t.Fatal("日限满不应轮转")
	}
	dec, detail := d406LastEval(t, s, p.ID)
	if dec != "hold_rotate_check" || !containsSub(detail, "日轮转限满") {
		t.Errorf("应拦阻于日限, got %s %q", dec, detail)
	}
}

// TestD406_RotatePairCooldown ④组合对冷却：近 24h 已向同 alt 轮转过 → 拦阻
// （maxPerDay=2 隔离日限谓词，专验冷却）。
func TestD406_RotatePairCooldown(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d406Seed(t, s, 15.0, true)
	if err := s.InsertPositionEval(ctx, pgstore.PositionEval{
		PositionID: p.ID, Decision: "rotate", Detail: "alt=" + d406AltOpp,
	}); err != nil {
		t.Fatalf("seed rotate eval: %v", err)
	}
	e := d406Engine(s, d406Quotes(time.Now().UTC(), 0), 2)
	close, _ := e.xassetExitCheck(ctx, p)
	if close {
		t.Fatal("组合对冷却中不应轮转")
	}
	_, detail := d406LastEval(t, s, p.ID)
	if !containsSub(detail, "冷却中") {
		t.Errorf("应拦阻于组合对冷却, got %q", detail)
	}
}

// TestD406_RotateFundingFailClosed ⑥目标 venue 资金数据缺（无快照）→ fail-closed 不轮转。
func TestD406_RotateFundingFailClosed(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d406Seed(t, s, 15.0, false) // withFunding=false：无快照无隐含率腿
	e := d406Engine(s, d406Quotes(time.Now().UTC(), 0), 1)
	close, _ := e.xassetExitCheck(ctx, p)
	if close {
		t.Fatal("资金数据缺应 fail-closed 不轮转")
	}
	_, detail := d406LastEval(t, s, p.ID)
	if !containsSub(detail, "资金数据缺") {
		t.Errorf("detail 应含资金数据缺拦阻因, got %q", detail)
	}
}

// TestD406_RotateStaleQuote 报价超龄 → 灾难闸标 stale → 轮转不评（观测行 fail_closed:stale）。
func TestD406_RotateStaleQuote(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d406Seed(t, s, 15.0, true)
	e := d406Engine(s, d406Quotes(time.Now().UTC(), 10*time.Minute), 1)
	close, _ := e.xassetExitCheck(ctx, p)
	if close {
		t.Fatal("报价超龄不轮转")
	}
	dec, _ := d406LastEval(t, s, p.ID)
	if dec != "fail_closed:stale" {
		t.Errorf("应落 fail_closed:stale（轮转未评）, got %s", dec)
	}
}

// TestD406_EarlierGateShortCircuits 前四闸任一触发 → 轮转不评（链序=先保命再优化）。
// carry_death：FundingExitPct=50 > curCarry 18.25 → breach 单次即平（debounce 缺省≤1）。
func TestD406_EarlierGateShortCircuits(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d406Seed(t, s, 15.0, true)
	e := d406Engine(s, d406Quotes(time.Now().UTC(), 0), 1)
	e.cfg.FundingExitPct = 50
	close, reason := e.xassetExitCheck(ctx, p)
	if !close {
		t.Fatal("carry_death 应触发平仓")
	}
	dec, _ := d406LastEval(t, s, p.ID)
	if dec != "carry_death" || containsSub(reason, "rotate") {
		t.Errorf("决策应为 carry_death 且不经轮转, got dec=%s reason=%q", dec, reason)
	}
}

// TestD406_RotateMissingCarry 当前仓 carry 数据缺（facts 不种）→ fail-closed。
func TestD406_RotateMissingCarry(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	// 只建仓+锚+monitor 行，不种 facts → recalcXAssetNetCarry ok=false。
	p := d404XAssetPos(t, s, d341fptr(-12.39), d341fptr(4.73))
	if err := s.UpdatePositionNotional(ctx, p.ID, 4000); err != nil {
		t.Fatalf("set notional: %v", err)
	}
	p = d406Relist(t, s, p.ID)
	d404SeedMonitorRowAt(t, s, p.OpenedAt.Add(-5*time.Second), 7.73, 4.73)
	e := d406Engine(s, d406Quotes(time.Now().UTC(), 0), 1)
	trig, out := e.rotateCheck(ctx, p)
	if trig {
		t.Fatal("carry 数据缺应 fail-closed")
	}
	if !containsSub(out.detail, "carry") {
		t.Errorf("detail 应含 carry 缺拦阻因, got %q", out.detail)
	}
}

// TestD406_RotateConfigDefault env 三处同步回归：config 默认 ARB_ROTATE_MAX_PER_DAY=1。
func TestD406_RotateConfigDefault(t *testing.T) {
	// envInt 默认 1 的断言走 config 包测试惯例——此处锚 Engine 侧语义：
	// maxPerDay≤0 = 不限（0 关闭日限）。
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d406Seed(t, s, 15.0, true)
	if err := s.InsertPositionEval(ctx, pgstore.PositionEval{
		PositionID: p.ID, Decision: "rotate", Detail: fmt.Sprintf("alt=%s", d406AltOpp),
	}); err != nil {
		t.Fatalf("seed rotate eval: %v", err)
	}
	// maxPerDay=0 → 日限关闭，但同 alt 冷却仍拦（谓词隔离：detail 应含冷却非日限）。
	e := d406Engine(s, d406Quotes(time.Now().UTC(), 0), 0)
	if close, _ := e.xassetExitCheck(ctx, p); close {
		t.Fatal("同 alt 冷却中不应轮转（日限关闭语义验证）")
	}
	_, detail := d406LastEval(t, s, p.ID)
	if !containsSub(detail, "冷却中") {
		t.Errorf("maxPerDay=0 应跳过日限直落冷却拦阻, got %q", detail)
	}
}
