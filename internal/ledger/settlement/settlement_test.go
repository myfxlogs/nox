package settlement

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// fakeDeps 测试用 settlement.Deps 实现。
type fakeDeps struct {
	position         *pgstore.Position
	legs             []pgstore.PositionLeg
	cs               float64
	csOk             bool
	fxRate           float64
	fxOk             bool
	hasSettlement    bool
	insertSettlement int
	insertWon        bool // 返修包②：模拟 InsertSettlement 返回 won
	insertFirstOnly  bool // 返修包②：true=仅第一次 won，后续 false（并发模拟）
	upsertVSample    int
	updateStat       int
	getStat          *pgstore.StrategyStat
	aggStat          pgstore.AggregateStat            // D-154 S1：AggregateStrategyStats 返回值
	aggStatByEnv     map[string]pgstore.AggregateStat // D-200：ByEnvironment 按 environment 返回不同 aggStat
	aggStatByFamily  map[string]pgstore.AggregateStat // D-256-A S2：族级聚合按 strategies[0] 返回不同 aggStat
	disableStrategy  string
	disabledStrats   []string   // D-256-A R1：族内全禁——记录所有被禁用策略
	cashflowsNet     float64    // D-127 P4 返修①：SumCashflowsNet 返回值
	lastNetPnl       float64    // D-127 P4：捕获传给 UpdateStrategyStat 的 net 值
	mu               sync.Mutex // 并发安全计数
	// 字段
	cashflowsQuality   string // SumCashflowsNetWithQuality 返回的 quality；空→按 cashflowsNet 推断
	cashflowsQueryErr  error  // SumCashflowsNetWithQuality 返回的 err（模拟查询失败）
	statsStatus        string // GetSettlementStatsStatus 返回值（applied/skipped 短路）
	markRetryableCount int    // MarkSettlementStatsRetryable 调用次数
	// 预测-实测对照 fake 面。
	insertedFlags     []pgstore.VenueSymbolFlag // InsertFlag 捕获
	activeFlags       map[string]pgstore.VenueSymbolFlag
	papers            []*pgstore.PaperSpotCarry // LatestPaperByLegs 按序返回首行（测试种一行）
	lastSettlementRow *pgstore.SettlementRow    // 捕获 InsertSettlement 收到的 row
	insertAlertMsgs   []string                  // D-268：InsertAlert 捕获的 level:message 列表（spy）
	// 字段
	gotHasFundingLeg bool // SumCashflowsNetForSettle 收到的 hasFundingLeg
	gotFundingDue    bool // SumCashflowsNetForSettle 收到的 fundingDue
	forSettleCalls   int  // SumCashflowsNetForSettle 调用次数
}

func (f *fakeDeps) GetPosition(ctx context.Context, id int64) (*pgstore.Position, error) {
	if f.position == nil {
		return nil, errors.New("not found")
	}
	return f.position, nil
}

func (f *fakeDeps) ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error) {
	return f.legs, nil
}

func (f *fakeDeps) ContractSizeByCanonical(ctx context.Context, venue, canonical string) (float64, bool, error) {
	return f.cs, f.csOk, nil
}

func (f *fakeDeps) USDPer(ctx context.Context, quoteCcy string, at time.Time) (float64, bool) {
	return f.fxRate, f.fxOk
}

func (f *fakeDeps) UpsertVerificationSample(ctx context.Context, vkey string, expectedUsd, realizedUsd float64, closedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertVSample++
	return nil
}

func (f *fakeDeps) UpdateStrategyStat(ctx context.Context, strategy string, realizedPnl float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateStat++
	f.lastNetPnl = realizedPnl // D-127 P4：捕获 net 值供断言
	return nil
}

func (f *fakeDeps) GetStrategyStat(ctx context.Context, strategy string) (*pgstore.StrategyStat, error) {
	if f.getStat == nil {
		return &pgstore.StrategyStat{Strategy: strategy}, nil
	}
	return f.getStat, nil
}

// AggregateStrategyStats 返回预设 aggStat（Strategy 填入参）。
func (f *fakeDeps) AggregateStrategyStats(ctx context.Context, strategy string) (pgstore.AggregateStat, error) {
	st := f.aggStat
	st.Strategy = strategy
	return st, nil
}

// ByEnvironment 测试 stub。aggStatByEnv 非空时按 environment 返回不同 aggStat
// （验证 environment 隔离）；空时委托 AggregateStrategyStats（忽略 environment 过滤）。
func (f *fakeDeps) AggregateStrategyStatsByEnvironment(ctx context.Context, strategy, environment string) (pgstore.AggregateStat, error) {
	if f.aggStatByEnv != nil {
		st, ok := f.aggStatByEnv[environment]
		if !ok {
			return pgstore.AggregateStat{Strategy: strategy}, nil
		}
		st.Strategy = strategy
		return st, nil
	}
	return f.AggregateStrategyStats(ctx, strategy)
}

// 族级聚合 stub。aggStatByFamily 非空时按 strategies[0] 返回预设值；
// 空时委托 AggregateStrategyStatsByEnvironment（向后兼容）。
func (f *fakeDeps) AggregateStrategyStatsByEnvironmentFamily(ctx context.Context, strategies []string, environment string) (pgstore.AggregateStat, error) {
	if len(strategies) == 0 {
		return pgstore.AggregateStat{}, nil
	}
	if f.aggStatByFamily != nil {
		st, ok := f.aggStatByFamily[strategies[0]]
		if !ok {
			return pgstore.AggregateStat{Strategy: strategies[0]}, nil
		}
		st.Strategy = strategies[0]
		return st, nil
	}
	return f.AggregateStrategyStatsByEnvironment(ctx, strategies[0], environment)
}

func (f *fakeDeps) DisableStrategy(ctx context.Context, strategy, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disableStrategy = strategy
	f.disabledStrats = append(f.disabledStrats, strategy) // D-256-A R1：族内全禁记录
	return nil
}

func (f *fakeDeps) InsertSettlement(ctx context.Context, r *pgstore.SettlementRow) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.insertSettlement++
	f.lastSettlementRow = r // D-193-A：捕获 row 供断言 completeness/stats_status
	if f.insertFirstOnly {
		return f.insertSettlement == 1, nil // 并发模拟：仅第一次 won
	}
	return f.insertWon, nil
}

func (f *fakeDeps) HasSettlement(ctx context.Context, posID int64) (bool, error) {
	return f.hasSettlement, nil
}

// 返修①：SumCashflowsNet 返回预设 cashflowsNet。
func (f *fakeDeps) SumCashflowsNet(ctx context.Context, posID int64) (float64, error) {
	return f.cashflowsNet, nil
}

// SumCashflowsNetWithQuality 返回 (cashflowsNet, quality, err)。
// cashflowsQuality 空 → 按 cashflowsNet 推断（非 0=actual，0=no_cashflows）。
func (f *fakeDeps) SumCashflowsNetWithQuality(ctx context.Context, posID int64) (float64, string, error) {
	if f.cashflowsQueryErr != nil {
		return 0, "", f.cashflowsQueryErr
	}
	q := f.cashflowsQuality
	if q == "" {
		if f.cashflowsNet != 0 {
			q = "actual"
		} else {
			q = "no_cashflows"
		}
	}
	return f.cashflowsNet, q, nil
}

// SumCashflowsNetForSettle 同预设语义 + 捕获 bool 入参/调用次数。
func (f *fakeDeps) SumCashflowsNetForSettle(ctx context.Context, posID int64, hasFundingLeg, fundingDue bool) (float64, string, error) {
	f.mu.Lock()
	f.gotHasFundingLeg, f.gotFundingDue = hasFundingLeg, fundingDue
	f.forSettleCalls++
	f.mu.Unlock()
	return f.SumCashflowsNetWithQuality(ctx, posID)
}

// 审计 P0-1：GetSettlementStatsStatus 返回预设 statsStatus（空=继续 Settle）。
func (f *fakeDeps) GetSettlementStatsStatus(ctx context.Context, posID int64) (string, error) {
	return f.statsStatus, nil
}

// 审计 P0-2：MarkSettlementStatsRetryable 计数。
func (f *fakeDeps) MarkSettlementStatsRetryable(ctx context.Context, posID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markRetryableCount++
	return nil
}

// 告警点 4：结算时策略淘汰告警 stub（spy：捕获 level:message 供测试断言）。
func (f *fakeDeps) InsertAlert(ctx context.Context, ruleID *int64, level, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.insertAlertMsgs = append(f.insertAlertMsgs, level+":"+message)
	return nil
}

// noElim 永不禁用的 Eliminator（测试用）。
func noElim(context.Context, int, float64, float64, float64, bool) (bool, string) { return false, "" }

// TestSettle_Idempotent 已有 position_settlements 行（stats_status=applied）→ 短路，不重复结算。
// 短路逻辑改为检查 stats_status（applied/skipped 短路）。
func TestSettle_Idempotent(t *testing.T) {
	d := &fakeDeps{statsStatus: "applied"}
	if err := Settle(context.Background(), d, 100, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.insertSettlement != 0 || d.upsertVSample != 0 || d.updateStat != 0 {
		t.Errorf("幂等短路失败：insert=%d upsert=%d updateStat=%d", d.insertSettlement, d.upsertVSample, d.updateStat)
	}
}

// TestSettle_HappyPath CLOSED + realized + expectedUsd>0 → 写 settlement + upsert + updateStat。
func TestSettle_HappyPath(t *testing.T) {
	now := time.Now()
	rp := 10.0
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:             1,
			OpportunityID:  "CROSS_VENUE:AUDJPY:52993526:mt5:277842155:mt5",
			Type:           "CROSS_VENUE",
			Status:         "CLOSED",
			ExpectedNetBps: 15.0,
			RealizedPnl:    &rp,
			OpenedAt:       &now,
			ClosedAt:       &now,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:        100000,
		csOk:      true,
		fxRate:    150,
		fxOk:      true,
		insertWon: true, // 返修包②：模拟赢家
	}
	if err := Settle(context.Background(), d, 1, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.insertSettlement != 1 {
		t.Errorf("insertSettlement=%d, want 1", d.insertSettlement)
	}
	if d.upsertVSample != 1 {
		t.Errorf("upsertVSample=%d, want 1", d.upsertVSample)
	}
	if d.updateStat != 1 {
		t.Errorf("updateStat=%d, want 1", d.updateStat)
	}
}

// TestSettle_SkipRealizedNULL realized_pnl NULL → 写 settlement 但 skipped，不入样本。
func TestSettle_SkipRealizedNULL(t *testing.T) {
	now := time.Now()
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:             2,
			OpportunityID:  "CROSS_VENUE:AUDJPY:52993526:mt5:277842155:mt5",
			Type:           "CROSS_VENUE",
			Status:         "CLOSED",
			ExpectedNetBps: 15.0,
			RealizedPnl:    nil,
			OpenedAt:       &now,
			ClosedAt:       &now,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:        100000,
		csOk:      true,
		fxRate:    150,
		fxOk:      true,
		insertWon: true, // 返修包②：模拟赢家（走 skipped 路径非并发输家）
	}
	if err := Settle(context.Background(), d, 2, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.insertSettlement != 1 {
		t.Errorf("insertSettlement=%d, want 1（跳过也写行）", d.insertSettlement)
	}
	if d.upsertVSample != 0 || d.updateStat != 0 {
		t.Errorf("跳过不应入样本：upsert=%d updateStat=%d", d.upsertVSample, d.updateStat)
	}
}

// TestSettle_SkipFxUnavailable fx 不可得 → skipped，不入样本。
func TestSettle_SkipFxUnavailable(t *testing.T) {
	now := time.Now()
	rp := 10.0
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:             3,
			OpportunityID:  "CROSS_VENUE:AUDJPY:52993526:mt5:277842155:mt5",
			Type:           "CROSS_VENUE",
			Status:         "CLOSED",
			ExpectedNetBps: 15.0,
			RealizedPnl:    &rp,
			OpenedAt:       &now,
			ClosedAt:       &now,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:        100000,
		csOk:      true,
		fxRate:    0,
		fxOk:      false, // JPY 汇率不可得
		insertWon: true,  // 返修包②：模拟赢家（走 skipped 路径非并发输家）
	}
	if err := Settle(context.Background(), d, 3, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.insertSettlement != 1 {
		t.Errorf("insertSettlement=%d, want 1", d.insertSettlement)
	}
	if d.upsertVSample != 0 {
		t.Errorf("fx 不可得应跳过样本：upsert=%d", d.upsertVSample)
	}
}

// TestSettle_EliminatorTriggered 淘汰判定禁用 → DisableStrategy 被调。
func TestSettle_EliminatorTriggered(t *testing.T) {
	now := time.Now()
	rp := -5.0
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:             4,
			OpportunityID:  "CROSS_VENUE:AUDJPY:52993526:mt5:277842155:mt5",
			Type:           "CROSS_VENUE",
			Status:         "CLOSED",
			ExpectedNetBps: 15.0,
			RealizedPnl:    &rp,
			OpenedAt:       &now,
			ClosedAt:       &now,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:        100000,
		csOk:      true,
		fxRate:    150,
		fxOk:      true,
		insertWon: true, // 返修包②：模拟赢家
		getStat:   &pgstore.StrategyStat{Strategy: "CROSS_VENUE", TotalTrades: 10, WinRate: 0.1, AvgPnl: -10},
	}
	alwaysElim := func(context.Context, int, float64, float64, float64, bool) (bool, string) {
		return true, "test elimination"
	}
	if err := Settle(context.Background(), d, 4, alwaysElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.disableStrategy != "CROSS_VENUE" {
		t.Errorf("disableStrategy=%q, want CROSS_VENUE", d.disableStrategy)
	}
}

// TestSettle_EliminatorEnvironmentIsolation D-200：Settle 淘汰检查按持仓 p.Environment 聚合，
// 不同 environment 返回不同 aggStat → 淘汰判定隔离。
// 场景①：p.Environment='demo'，aggStatByEnv['demo']={Total:10, AvgPnl:-10} → elim 触发 → 禁用。
// 场景②：p.Environment='live'，aggStatByEnv['live']={Total:10, AvgPnl:+10} → elim 不触发 → 不禁用。
// 验证 Settle 把 p.Environment 透传给 AggregateStrategyStatsByEnvironment（而非忽略 environment）。
func TestSettle_EliminatorEnvironmentIsolation(t *testing.T) {
	now := time.Now()
	rp := -5.0
	elimByAvg := func(_ context.Context, total int, winRate, avg, median float64, disabled bool) (bool, string) {
		if avg < 0 {
			return true, "avg<0"
		}
		return false, ""
	}

	// 场景①：demo environment → aggStat avg<0 → 禁用
	dDemo := &fakeDeps{
		position: &pgstore.Position{
			ID:             6,
			OpportunityID:  "CROSS_VENUE:AUDJPY:52993526:mt5:277842155:mt5",
			Type:           "CROSS_VENUE",
			Status:         "CLOSED",
			ExpectedNetBps: 15.0,
			RealizedPnl:    &rp,
			OpenedAt:       &now,
			ClosedAt:       &now,
			Environment:    "demo",
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:        100000,
		csOk:      true,
		fxRate:    150,
		fxOk:      true,
		insertWon: true,
		aggStatByEnv: map[string]pgstore.AggregateStat{
			"demo": {Total: 10, AvgPnl: -10, MedianPnl: -10},
			"live": {Total: 10, AvgPnl: 10, MedianPnl: 10},
		},
	}
	if err := Settle(context.Background(), dDemo, 6, elimByAvg, nil); err != nil {
		t.Fatalf("Settle demo: %v", err)
	}
	if dDemo.disableStrategy != "CROSS_VENUE" {
		t.Errorf("demo: disableStrategy=%q, want CROSS_VENUE（avg<0 应触发淘汰）", dDemo.disableStrategy)
	}

	// 场景②：live environment → aggStat avg>0 → 不禁用
	dLive := &fakeDeps{
		position: &pgstore.Position{
			ID:             7,
			OpportunityID:  "CROSS_VENUE:AUDJPY:52993526:mt5:277842155:mt5",
			Type:           "CROSS_VENUE",
			Status:         "CLOSED",
			ExpectedNetBps: 15.0,
			RealizedPnl:    &rp,
			OpenedAt:       &now,
			ClosedAt:       &now,
			Environment:    "live",
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:        100000,
		csOk:      true,
		fxRate:    150,
		fxOk:      true,
		insertWon: true,
		aggStatByEnv: map[string]pgstore.AggregateStat{
			"demo": {Total: 10, AvgPnl: -10, MedianPnl: -10},
			"live": {Total: 10, AvgPnl: 10, MedianPnl: 10},
		},
	}
	if err := Settle(context.Background(), dLive, 7, elimByAvg, nil); err != nil {
		t.Fatalf("Settle live: %v", err)
	}
	if dLive.disableStrategy != "" {
		t.Errorf("live: disableStrategy=%q, want \"\"（avg>0 不应触发淘汰）", dLive.disableStrategy)
	}
}

func ptrFloat(v float64) *float64 { return &v }

// TestSettle_ConcurrentLoser 返修包②：并发双计窗口防护。
// 两个 goroutine 同 posID 并发 Settle——HasSettlement 短路都通过后，
// InsertSettlement 只有一个 won=true（赢家），另一个 won=false（输家）→ return nil 不走步骤 7/8。
// 断言：upsertVSample 只 +1（不是 2）、updateStat 只 +1（不是 2）。
func TestSettle_ConcurrentLoser(t *testing.T) {
	now := time.Now()
	rp := 10.0
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:             5,
			OpportunityID:  "CROSS_VENUE:AUDJPY:52993526:mt5:277842155:mt5",
			Type:           "CROSS_VENUE",
			Status:         "CLOSED",
			ExpectedNetBps: 15.0,
			RealizedPnl:    &rp,
			OpenedAt:       &now,
			ClosedAt:       &now,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:              100000,
		csOk:            true,
		fxRate:          150,
		fxOk:            true,
		insertFirstOnly: true, // 并发模拟：仅第一次 InsertSettlement won=true
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = Settle(context.Background(), d, 5, noElim, nil)
		}()
	}
	wg.Wait()

	if d.insertSettlement != 2 {
		t.Errorf("insertSettlement=%d, want 2（两 goroutine 都调 InsertSettlement）", d.insertSettlement)
	}
	// 核心断言：只有赢家走步骤 8，输家 return nil → 样本/统计只 +1
	if d.upsertVSample != 1 {
		t.Errorf("upsertVSample=%d, want 1（输家不应双计）", d.upsertVSample)
	}
	if d.updateStat != 1 {
		t.Errorf("updateStat=%d, want 1（输家不应双计）", d.updateStat)
	}
}

// TestComputeNotionalUSD_AUDJPYAnchor D-087 返修③ 端到端锚点（防双重求逆回归）。
// AUDJPY 1手/CS100000/¥113.517/USDJPY=158.87 → USDPer(JPY)=1/158.87（USD per JPY）。
// 单腿 notional = 1×100000×113.517×(1/158.87) = 11,351,700/158.87 ≈ $71,456。
// 双腿 ≈ $142,891（与 #266 手工真值同口径）。
// 旧代码 fx=1.0/rate 把 1/158.87 求逆回 158.87 → notional ×159 ≈ $11.4M（#266 湿跑 $3.6B/2腿）。
func TestComputeNotionalUSD_AUDJPYAnchor(t *testing.T) {
	const (
		usdJpy = 158.87
		cs     = 100000.0
		price  = 113.517
	)
	// USDPer 契约：返回 USD per quoteCcy。JPY → 1/USDJPY = 1/158.87。
	usdPerJpy := 1.0 / usdJpy
	d := &fakeDeps{
		cs:     cs,
		csOk:   true,
		fxRate: usdPerJpy,
		fxOk:   true,
	}
	// 单腿
	single := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(price)},
	}
	got, skipped := computeNotionalUSD(context.Background(), d, single, "AUDJPY")
	if skipped {
		t.Fatalf("单腿 skipped=true，期望 fx 可得")
	}
	const wantSingle = 11351700.0 / usdJpy // ≈ 71455.6
	if !approx(got, wantSingle, 1.0) {
		t.Errorf("单腿 notional=%.2f, want ≈%.2f (±1)", got, wantSingle)
	}
	t.Logf("单腿 notional=$%.2f (want ≈$71,456)", got)

	// 双腿（对称 SWAPARB 两腿）
	double := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(price)},
		{Phase: "OPEN", Status: "FILLED", Venue: "277842155:mt5", Qty: 1.0, PriceActual: ptrFloat(price)},
	}
	got2, skipped2 := computeNotionalUSD(context.Background(), d, double, "AUDJPY")
	if skipped2 {
		t.Fatalf("双腿 skipped=true，期望 fx 可得")
	}
	const wantDouble = 2 * 11351700.0 / usdJpy // ≈ 142891.2
	if !approx(got2, wantDouble, 2.0) {
		t.Errorf("双腿 notional=%.2f, want ≈%.2f (±2)", got2, wantDouble)
	}
	t.Logf("双腿 notional=$%.2f (want ≈$142,891)", got2)

	// SWAPARB expected = swapBpsDaily/10000 × notionalUSD × holdDays
	// 0.4713/10000 × 142891 × 1 ≈ $6.74（与 #266 文档真值同口径）
	const swapBpsDaily = 0.4713
	expectedUsd := swapBpsDaily / 10000 * got2 * 1.0
	const wantExpected = 0.4713 / 10000 * 142891.2
	if !approx(expectedUsd, wantExpected, 0.05) {
		t.Errorf("SWAPARB expected=%.4f, want ≈%.4f", expectedUsd, wantExpected)
	}
	t.Logf("SWAPARB expected=$%.4f (holdDays=1, swapBpsDaily=0.4713)", expectedUsd)

	// 回归断言：旧双重求逆会得到 ≈$11.4M/腿（×158.87），确保现值远小于该值。
	if got > wantSingle*10 {
		t.Errorf("单腿 notional=%.2f 远超预期，疑似双重求逆回归（×USDJPY）", got)
	}
}

// approx 绝对误差容差。
func approx(got, want, tol float64) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// 返修①：net = realized + cashflowsNet（swap+funding−commission）纳入淘汰/复盘口径。
// 场景：SWAPARB realized=-1（价差小负）+ cashflowsNet=+3（息差正收）→ net=+2 → stats 记 win。
// 旧口径（裸 realized）→ stats 记 loss → 平 5 笔触发 avgPnl<0 自动淘汰误杀金鹅。
func TestD127_SettleNetIncludesSwap(t *testing.T) {
	opened := time.Now().AddDate(0, 0, -7) // 持仓 7 天
	closed := time.Now()
	rp := -1.0
	swapBpsDaily := 0.5
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:                   10,
			OpportunityID:        "SWAPARB:AUDJPY:52993526:mt5:277842155:mt5",
			Type:                 "SWAPARB",
			Status:               "CLOSED",
			ExpectedNetBps:       0.5,
			ExpectedSwapBpsDaily: &swapBpsDaily,
			RealizedPnl:          &rp,
			OpenedAt:             &opened,
			ClosedAt:             &closed,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:           100000,
		csOk:         true,
		fxRate:       150,
		fxOk:         true,
		insertWon:    true,
		cashflowsNet: 3.0, // swap+funding−commission 实收 +$3（D-013：组件列用，不再加进 net）
	}
	if err := Settle(context.Background(), d, 10, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	// ②：realized_pnl 已是单源净额（exec 侧 PositionNetUSD 写入）→ net=-1 原样传播
	if !approx(d.lastNetPnl, -1.0, 0.001) {
		t.Errorf("UpdateStrategyStat net=%.4f，期望 -1.0（realized_pnl=净额单源）", d.lastNetPnl)
	}
	if d.updateStat != 1 {
		t.Errorf("updateStat=%d，期望 1", d.updateStat)
	}
}

// 返修①：cashflowsNet=0（无 swap/funding/commission 行）→ net=realized（旧口径兼容）。
func TestD127_SettleNetNoSwap(t *testing.T) {
	now := time.Now()
	rp := 5.0
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:             11,
			OpportunityID:  "CROSS_VENUE:BTCUSDT:okx:perp:binance:perp",
			Type:           "CROSS_VENUE",
			Status:         "CLOSED",
			ExpectedNetBps: 10.0,
			RealizedPnl:    &rp,
			OpenedAt:       &now,
			ClosedAt:       &now,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "okx:perp", Qty: 0.1, PriceActual: ptrFloat(100000)},
		},
		cs:           1,
		csOk:         false,
		fxRate:       1,
		fxOk:         true,
		insertWon:    true,
		cashflowsNet: 0, // 无 swap/funding/commission
	}
	if err := Settle(context.Background(), d, 11, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if !approx(d.lastNetPnl, 5.0, 0.001) {
		t.Errorf("UpdateStrategyStat net=%.4f，期望 5.0（无 swap，net=realized）", d.lastNetPnl)
	}
}

// TestSettle_NoDoubleCount D-159 W3a R4 A5：双计拆除后 netPnl = 净值一次合成（无双计）。
// S2 后 realized_pnl = 纯价格差 PnL（不含 swap/commission），cashflowsNet = swap+commission 组件。
// 场景：realized_pnl=100（价格差）+ cashflowsNet=3（swap 5 + commission -2）→ netPnl=103（非 106 双计）。
// 旧双计口径：realized_pnl=103（含 swap/commission）+ cashflowsNet=3 → 106（swap/commission 各计两次）。
func TestSettle_NoDoubleCount(t *testing.T) {
	opened := time.Now().AddDate(0, 0, -7) // 持仓 7 天（确保 expectedUsd>0）
	closed := time.Now()
	rp := 100.0
	swapBpsDaily := 0.5
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:                   20,
			OpportunityID:        "SWAPARB:AUDJPY:52993526:mt5:277842155:mt5",
			Type:                 "SWAPARB",
			Status:               "CLOSED",
			ExpectedNetBps:       15.0,
			ExpectedSwapBpsDaily: &swapBpsDaily,
			RealizedPnl:          &rp, // 100 = 纯价格差（S2 后 calcLegPnL 不含 swap/commission）
			OpenedAt:             &opened,
			ClosedAt:             &closed,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:           100000,
		csOk:         true,
		fxRate:       150,
		fxOk:         true,
		insertWon:    true,
		cashflowsNet: 3.0, // swap 5 + commission -2 = 3（D-013：组件列/quality 判据用）
	}
	if err := Settle(context.Background(), d, 20, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	// ②：netPnl=realized_pnl（PositionNetUSD 单源净额已含现金流）原样传播=100。
	// 旧口径 100+3=103 在单源下即双计——本用例反转即回归锚。
	if !approx(d.lastNetPnl, 100.0, 0.001) {
		t.Errorf("netPnl=%.4f, want 100.0 (realized_pnl=净额单源)", d.lastNetPnl)
	}
}

// TestD013_ExternalFeeNullPartial D-013 ②：EXTERNAL 平仓腿 fee_usd NULL →
// completeness=partial、stats_status=retryable（不入正式样本，venue 补报可升级）。
func TestD013_ExternalFeeNullPartial(t *testing.T) {
	now := time.Now()
	rp := 5.0
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:             30,
			OpportunityID:  "CROSS_VENUE:BTCUSDT:okx:perp:binance:perp",
			Type:           "CROSS_VENUE",
			Status:         "CLOSED",
			ExpectedNetBps: 10.0,
			RealizedPnl:    &rp,
			OpenedAt:       &now,
			ClosedAt:       &now,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "okx:perp", Qty: 0.1, PriceActual: ptrFloat(100000)},
			{Phase: "CLOSE", Status: "EXTERNAL", Venue: "okx:perp", Qty: 0.1, PriceActual: ptrFloat(100100)},
		},
		cs:        1,
		csOk:      false,
		fxRate:    1,
		fxOk:      true,
		insertWon: true,
	}
	if err := Settle(context.Background(), d, 30, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.lastSettlementRow == nil {
		t.Fatal("no settlement row")
	}
	if d.lastSettlementRow.CompletenessStatus != "partial" {
		t.Errorf("completeness=%s, want partial", d.lastSettlementRow.CompletenessStatus)
	}
	if d.lastSettlementRow.StatsStatus != "retryable" {
		t.Errorf("stats_status=%s, want retryable", d.lastSettlementRow.StatsStatus)
	}
	if d.upsertVSample != 0 || d.updateStat != 0 {
		t.Errorf("partial must not enter formal stats: upsert=%d update=%d", d.upsertVSample, d.updateStat)
	}
}

// TestD013R1_CexFilledFeeNullPartial D-013 R1 ②：CEX FILLED 平仓腿 fee NULL → partial；
// MT FILLED 腿 fee NULL 不算缺（恒显式 0 commission 行）→ complete。
func TestD013R1_CexFilledFeeNullPartial(t *testing.T) {
	now := time.Now()
	rp := 5.0
	mk := func(venue string) *fakeDeps {
		return &fakeDeps{
			position: &pgstore.Position{
				ID: 31, OpportunityID: "CROSS_VENUE:BTCUSDT:okx:perp:binance:perp",
				Type: "CROSS_VENUE", Status: "CLOSED", ExpectedNetBps: 10.0,
				RealizedPnl: &rp, OpenedAt: &now, ClosedAt: &now,
			},
			legs: []pgstore.PositionLeg{
				{Phase: "OPEN", Status: "FILLED", Venue: venue, Qty: 0.1, PriceActual: ptrFloat(100000)},
				{Phase: "CLOSE", Status: "FILLED", Venue: venue, Qty: 0.1, PriceActual: ptrFloat(100100)},
			},
			cs: 1, csOk: false, fxRate: 1, fxOk: true, insertWon: true,
		}
	}
	dCEX := mk("okx:perp")
	if err := Settle(context.Background(), dCEX, 31, noElim, nil); err != nil {
		t.Fatalf("Settle CEX: %v", err)
	}
	if dCEX.lastSettlementRow.CompletenessStatus != "partial" ||
		dCEX.lastSettlementRow.StatsStatus != "retryable" {
		t.Fatalf("CEX FILLED fee NULL: completeness=%s stats=%s, want partial/retryable",
			dCEX.lastSettlementRow.CompletenessStatus, dCEX.lastSettlementRow.StatsStatus)
	}
	dMT := mk("3001157:mt5")
	if err := Settle(context.Background(), dMT, 31, noElim, nil); err != nil {
		t.Fatalf("Settle MT: %v", err)
	}
	if dMT.lastSettlementRow.CompletenessStatus != "complete" {
		t.Fatalf("MT FILLED fee NULL: completeness=%s, want complete",
			dMT.lastSettlementRow.CompletenessStatus)
	}
}

// TestSettle_EstimatedSkip D-159 W3a R4 A10：note 含 "pnl_estimated" → skippedReason 非空，不入正式统计。
// 场景：position note="pnl_estimated: partial or no calibration"（calibrator 接线但部分校准失败标记）。
// expectedUsd>0（多日持仓）确保跳过由 pnl_estimated 触发，非 expected_usd<=0。
func TestSettle_EstimatedSkip(t *testing.T) {
	opened := time.Now().AddDate(0, 0, -7) // 持仓 7 天（确保 expectedUsd>0）
	closed := time.Now()
	rp := 10.0
	swapBpsDaily := 0.5
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:                   21,
			OpportunityID:        "SWAPARB:AUDJPY:52993526:mt5:277842155:mt5",
			Type:                 "SWAPARB",
			Status:               "CLOSED",
			ExpectedNetBps:       15.0,
			ExpectedSwapBpsDaily: &swapBpsDaily,
			RealizedPnl:          &rp,
			Note:                 "pnl_estimated: partial or no calibration",
			OpenedAt:             &opened,
			ClosedAt:             &closed,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:        100000,
		csOk:      true,
		fxRate:    150,
		fxOk:      true,
		insertWon: true,
	}
	if err := Settle(context.Background(), d, 21, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	// estimated position 跳过：不入样本/统计（由 pnl_estimated 触发，非 expected_usd<=0）
	if d.upsertVSample != 0 {
		t.Errorf("estimated position upsertVSample=%d, want 0 (skipped by pnl_estimated, not in formal stats)", d.upsertVSample)
	}
	if d.updateStat != 0 {
		t.Errorf("estimated position updateStat=%d, want 0 (skipped by pnl_estimated, not in formal stats)", d.updateStat)
	}
}

// TestSettleAt_DeterministicSettledAt D-194 §3.3：同一 now → settlement 行 SettledAt=now；不同 now → 不同 SettledAt。
// 利用现有 fakeDeps.lastSettlementRow 捕获断言。
func TestSettleAt_DeterministicSettledAt(t *testing.T) {
	now1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	now2 := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)

	// 第一次：now1
	d1 := &fakeDeps{
		position: &pgstore.Position{ID: 200, Type: "SWAPARB", Status: "CLOSED", OpportunityID: "opp-200"},
	}
	if err := SettleAt(context.Background(), d1, 200, noElim, nil, now1); err != nil {
		t.Fatalf("SettleAt(now1): %v", err)
	}
	if d1.lastSettlementRow == nil {
		t.Fatal("lastSettlementRow=nil, want non-nil")
	}
	if !d1.lastSettlementRow.SettledAt.Equal(now1) {
		t.Errorf("SettledAt=%v, want %v (now1)", d1.lastSettlementRow.SettledAt, now1)
	}

	// 第二次：now2 → 不同 SettledAt
	d2 := &fakeDeps{
		position: &pgstore.Position{ID: 201, Type: "SWAPARB", Status: "CLOSED", OpportunityID: "opp-201"},
	}
	if err := SettleAt(context.Background(), d2, 201, noElim, nil, now2); err != nil {
		t.Fatalf("SettleAt(now2): %v", err)
	}
	if d2.lastSettlementRow == nil {
		t.Fatal("lastSettlementRow=nil, want non-nil")
	}
	if !d2.lastSettlementRow.SettledAt.Equal(now2) {
		t.Errorf("SettledAt=%v, want %v (now2)", d2.lastSettlementRow.SettledAt, now2)
	}
	if d1.lastSettlementRow.SettledAt.Equal(d2.lastSettlementRow.SettledAt) {
		t.Errorf("两次 SettledAt 相同，应不同（now1≠now2）")
	}
}

// TestSettle_CompatWrapper_DelegatesToSettleAt D-194 §3.3：验证 Settle 内部委托 SettleAt。
// 通过行为等价验证（Settle 调用后 fakeDeps.lastSettlementRow 非 nil），不断言 SettledAt 精确值。
func TestSettle_CompatWrapper_DelegatesToSettleAt(t *testing.T) {
	d := &fakeDeps{
		position: &pgstore.Position{ID: 300, Type: "SWAPARB", Status: "CLOSED", OpportunityID: "opp-300"},
	}
	if err := Settle(context.Background(), d, 300, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.lastSettlementRow == nil {
		t.Fatal("lastSettlementRow=nil, want non-nil (Settle should delegate to SettleAt)")
	}
}

// TestD256A_S2_FamilyAggregationUsed D-256-A S2：family 非 nil 时 Settle 用族级聚合。
// CARRY 持仓 + family resolver → AggregateStrategyStatsByEnvironmentFamily 被调用（而非 ByEnvironment）。
// aggStatByFamily["CARRY"]={Total:10, AvgPnl:-10} → elim 触发 → 禁用。
func TestD256A_S2_FamilyAggregationUsed(t *testing.T) {
	now := time.Now()
	openAt := now.Add(-24 * time.Hour) // holdDays=1
	rp := -5.0
	d := &fakeDeps{
		position: &pgstore.Position{
			ID: 400, Type: "CARRY", Status: "CLOSED", OpportunityID: "CARRY:AUDJPY:52993526:mt5:277842155:mt5",
			RealizedPnl:    &rp,
			ExpectedNetBps: 10.0, // CARRY 用 ExpectedNetBps 算 ExpectedUsd（>0 不被 skip）
			OpenedAt:       &openAt,
			ClosedAt:       &now,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:        100000,
		csOk:      true,
		fxRate:    150,
		fxOk:      true,
		insertWon: true,
		aggStatByFamily: map[string]pgstore.AggregateStat{
			"CARRY": {Total: 10, Wins: 1, AvgPnl: -10, MedianPnl: -10},
		},
		cashflowsNet:     1.0, // CARRY 有 swap 现金流
		cashflowsQuality: "actual",
	}
	elimByAvg := func(_ context.Context, total int, winRate, avg, median float64, disabled bool) (bool, string) {
		if avg < 0 {
			return true, "family avg<0"
		}
		return false, ""
	}
	// family resolver: CARRY → [CARRY, SWAPARB]
	family := func(strategy string) []string {
		if strategy == "CARRY" || strategy == "SWAPARB" {
			return []string{"CARRY", "SWAPARB"}
		}
		return []string{strategy}
	}
	if err := Settle(context.Background(), d, 400, elimByAvg, family); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	// 族内全禁——CARRY 触发淘汰时 SWAPARB 也被禁用。
	if len(d.disabledStrats) != 2 {
		t.Fatalf("R1 family disable: want 2 disabled [CARRY SWAPARB], got %v", d.disabledStrats)
	}
	got := map[string]bool{}
	for _, s := range d.disabledStrats {
		got[s] = true
	}
	if !got["CARRY"] || !got["SWAPARB"] {
		t.Errorf("R1 family disable: want [CARRY SWAPARB], got %v", d.disabledStrats)
	}
}

// TestD256A_S2_FamilyNilFallsBackToByEnvironment D-256-A S2：family=nil 时回退 ByEnvironment。
// 验证向后兼容——不传 family 时行为不变。
func TestD256A_S2_FamilyNilFallsBackToByEnvironment(t *testing.T) {
	now := time.Now()
	openAt := now.Add(-24 * time.Hour) // holdDays=1
	rp := -5.0
	d := &fakeDeps{
		position: &pgstore.Position{
			ID: 401, Type: "CARRY", Status: "CLOSED", OpportunityID: "CARRY:AUDJPY:52993526:mt5:277842155:mt5",
			RealizedPnl:    &rp,
			ExpectedNetBps: 10.0, // CARRY 用 ExpectedNetBps 算 ExpectedUsd（>0 不被 skip）
			OpenedAt:       &openAt,
			ClosedAt:       &now,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:               100000,
		csOk:             true,
		fxRate:           150,
		fxOk:             true,
		insertWon:        true,
		aggStat:          pgstore.AggregateStat{Total: 5, Wins: 1, AvgPnl: -10, MedianPnl: -10},
		cashflowsNet:     1.0, // CARRY 有 swap 现金流
		cashflowsQuality: "actual",
	}
	elimByAvg := func(_ context.Context, total int, winRate, avg, median float64, disabled bool) (bool, string) {
		if avg < 0 {
			return true, "avg<0"
		}
		return false, ""
	}
	// family=nil → 回退 ByEnvironment → 用 aggStat（fakeDeps ByEnvironment 空时委托 AggregateStrategyStats）
	if err := Settle(context.Background(), d, 401, elimByAvg, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.disableStrategy != "CARRY" {
		t.Errorf("disableStrategy=%q, want CARRY (ByEnvironment fallback)", d.disableStrategy)
	}
}

// fake 三法。
func (f *fakeDeps) InsertFlag(ctx context.Context, fl *pgstore.VenueSymbolFlag) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.insertedFlags = append(f.insertedFlags, *fl)
	return nil
}
func (f *fakeDeps) ListActiveFlags(ctx context.Context) (map[string]pgstore.VenueSymbolFlag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeFlags, nil
}
func (f *fakeDeps) LatestPaperByLegs(ctx context.Context, canonical string, legVenues []string) (*pgstore.PaperSpotCarry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.papers {
		if p.Symbol == canonical {
			return p, nil
		}
	}
	return nil, nil
}

// AppConfigFloat D-180：测试桩（缺省=键不存在→Zσ 回落默认 2）。
func (f *fakeDeps) AppConfigFloat(context.Context, string) (float64, bool, error) {
	return 0, false, nil
}
