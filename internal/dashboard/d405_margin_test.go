// d405_margin_test.go —— D-405 S5：三档判定纯函数矩阵 + 冷却持久化 + 评估器 DSN 锚。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

func f64p(v float64) *float64 { return &v }

// TestD405_EvalMarginTier_MT MT 族三档边界：free_margin < 3×/1.5×Σ(名义×日波动)。
func TestD405_EvalMarginTier_MT(t *testing.T) {
	vol := 100.0 // Σ(腿名义×日波动)=$100/日 → warn 线 $300 / urgent 线 $150
	cases := []struct {
		name       string
		freeMargin float64
		wantTier   string
		wantCov    float64
	}{
		{"urgent 下界内", 100, "urgent", 1.0},
		{"urgent 边界上=warn", 149.9, "urgent", 1.499},
		{"warn 带中", 200, "warn", 2.0},
		{"warn 上界=normal", 300.5, "normal", 3.005},
		{"normal 充裕", 900, "normal", 9.0},
	}
	for _, c := range cases {
		out := evalMarginTier(MarginEvalIn{
			VenueType: "mt5", HasLegs: true,
			Equity: f64p(5000), FreeMargin: f64p(c.freeMargin), DailyVolUSD: vol,
		})
		if out.Tier != c.wantTier {
			t.Errorf("%s: tier=%s want %s", c.name, out.Tier, c.wantTier)
		}
		if out.CoverageDays < c.wantCov-0.01 || out.CoverageDays > c.wantCov+0.01 {
			t.Errorf("%s: coverage=%.3f want ~%.3f", c.name, out.CoverageDays, c.wantCov)
		}
	}
}

// TestD405_EvalMarginTier_MT_DataStale MT 数据缺 → data_stale（禁陈旧绿）。
func TestD405_EvalMarginTier_MT_DataStale(t *testing.T) {
	for name, in := range map[string]MarginEvalIn{
		"equity 未采":     {VenueType: "mt5", HasLegs: true, FreeMargin: f64p(100), DailyVolUSD: 10},
		"freeMargin 未采": {VenueType: "mt5", HasLegs: true, Equity: f64p(5000), DailyVolUSD: 10},
		"双缺":            {VenueType: "mt5", HasLegs: true, DailyVolUSD: 10},
	} {
		if out := evalMarginTier(in); out.Tier != "data_stale" {
			t.Errorf("%s: tier=%s want data_stale", name, out.Tier)
		}
	}
	// 无腿 venue：字段齐 → normal（空仓无保证金风险，无腿波动求和义务）
	if out := evalMarginTier(MarginEvalIn{
		VenueType: "mt5", HasLegs: false, Equity: f64p(5000), FreeMargin: f64p(10), DailyVolUSD: 0,
	}); out.Tier != "normal" {
		t.Errorf("无腿 MT: tier=%s want normal", out.Tier)
	}
}

// TestD405_EvalMarginTier_CEX CEX 族：buffer_multiple 阈值向 <N（R0-B1 归一）。
func TestD405_EvalMarginTier_CEX(t *testing.T) {
	cases := []struct {
		name    string
		buffer  float64
		hasLegs bool
		want    string
	}{
		{"urgent <1.5", 1.2, true, "urgent"},
		{"warn <3", 2.5, true, "warn"},
		{"normal ≥3", 5.0, true, "normal"},
		{"无腿 normal", 0.0, false, "normal"}, // 空仓无 MMR 风险（buffer 未采也 normal）
	}
	for _, c := range cases {
		in := MarginEvalIn{VenueType: "unified", HasLegs: c.hasLegs, Equity: f64p(10000)}
		if c.hasLegs {
			in.Buffer = f64p(c.buffer)
			in.DailyVolUSD = 50
		}
		out := evalMarginTier(in)
		if out.Tier != c.want {
			t.Errorf("%s: tier=%s want %s", c.name, out.Tier, c.want)
		}
	}
	// 有腿但 buffer 未采 → data_stale（采集洞禁陈旧绿）
	out := evalMarginTier(MarginEvalIn{
		VenueType: "unified", HasLegs: true, Equity: f64p(10000), DailyVolUSD: 50,
	})
	if out.Tier != "data_stale" {
		t.Errorf("CEX 有腿无 buffer: tier=%s want data_stale", out.Tier)
	}
	// CEX 建议补充=拉回 buffer≥3 缺口：buffer=2 → mmr=5000，缺口=3×5000−10000=5000
	out = evalMarginTier(MarginEvalIn{
		VenueType: "unified", HasLegs: true, Equity: f64p(10000), Buffer: f64p(2), DailyVolUSD: 50,
	})
	if out.SuggestedTopupUSD != 5000 {
		t.Errorf("cex topup=%.0f want 5000", out.SuggestedTopupUSD)
	}
}

// TestD405_EvalMarginTier_NonMargin 现货/资金账户恒 normal（SSOT §1 刻意不做 margin 档）。
func TestD405_EvalMarginTier_NonMargin(t *testing.T) {
	if out := evalMarginTier(MarginEvalIn{VenueType: "spot", HasLegs: true}); out.Tier != "normal" {
		t.Errorf("spot tier=%s want normal", out.Tier)
	}
}

// TestD405_ShouldMarginAlert 冷却+去抖矩阵（urgent 连续 2 tick / 分档冷却 / 换档重发）。
func TestD405_ShouldMarginAlert(t *testing.T) {
	now := time.Now()
	past := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	urgentCD, warnCD := 30*time.Minute, 6*time.Hour

	cases := []struct {
		name      string
		tier      string
		prevTier  string
		lastAlert *time.Time
		streak    int
		want      bool
	}{
		{"urgent 首 tick 去抖", "urgent", "warn", nil, 1, false},
		{"urgent 第 2 tick 发", "urgent", "urgent", nil, 2, true},
		{"urgent 冷却内不发", "urgent", "urgent", past(10 * time.Minute), 3, false},
		{"urgent 冷却过发", "urgent", "urgent", past(40 * time.Minute), 2, true},
		{"warn 首发", "warn", "normal", nil, 0, true},
		{"warn 冷却内不发", "warn", "warn", past(3 * time.Hour), 0, false},
		{"warn 冷却过发", "warn", "warn", past(7 * time.Hour), 0, true},
		{"normal 永不发", "normal", "urgent", nil, 0, false},
		{"data_stale 永不发", "data_stale", "warn", nil, 0, false},
		{"urgent→warn 换档新窗口", "warn", "urgent", past(1 * time.Minute), 0, true},
	}
	for _, c := range cases {
		got := shouldMarginAlert(c.tier, c.prevTier, c.lastAlert, c.streak, now, urgentCD, warnCD)
		if got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// ---- DSN 锚：evaluate 全链路（腿+snapshot+ticks → fin_venue_margin_status upsert + 冷却持久化） ----

type d405Fixture struct {
	s   *pgstore.Store
	ctx context.Context
}

func d405Store(t *testing.T) *pgstore.Store {
	t.Helper()
	s := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	for _, tbl := range []string{
		"fin_venue_margin_status", "fin_venue_account_snapshot",
		"position_legs", "positions", "ticks", "broker_symbol_map",
	} {
		if _, err := s.Pool().Exec(ctx, "TRUNCATE TABLE "+tbl+" RESTART IDENTITY CASCADE"); err != nil {
			t.Fatalf("TRUNCATE %s: %v", tbl, err)
		}
	}
	return s
}

// d405SeedTicks 播 ticks 使 TickVolatility24h 达标：24h 窗口内 spread 覆盖 ≥2h，
// bid 100→110 半幅 (110−100)/2/105 ≈ 0.0476。
func d405SeedTicks(t *testing.T, s *pgstore.Store, venue, symbol string) {
	t.Helper()
	now := time.Now()
	if err := s.EnsureMonthPartition(context.Background(), now.Year(), int(now.Month())); err != nil {
		t.Fatalf("EnsureMonthPartition: %v", err)
	}
	if err := s.InsertTicks(context.Background(), []pgstore.TickRow{
		{Ts: now.Add(-3 * time.Hour), Venue: venue, VenueType: "mt5", Symbol: symbol, Bid: 100, Ask: 100.1},
		{Ts: now.Add(-30 * time.Minute), Venue: venue, VenueType: "mt5", Symbol: symbol, Bid: 110, Ask: 110.1},
	}); err != nil {
		t.Fatalf("seed ticks: %v", err)
	}
}

func d405SeedMTLeg(t *testing.T, s *pgstore.Store, venueKey, symbol, oppID string, qty, price float64) int64 {
	t.Helper()
	ctx := context.Background()
	// D-430：MT 腿 tick 键经 broker_symbol_map 解析 canonical——测试腿符号本身即 canonical。
	parts := strings.Split(venueKey, ":")
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES ($1,$2,'h',443,'p') ON CONFLICT (venue) DO NOTHING`,
		parts[0], parts[1]); err != nil {
		t.Fatalf("seed broker account: %v", err)
	}
	if err := s.UpsertSymbolMapping(ctx, &pgstore.SymbolMapping{
		Venue: parts[0], BrokerSymbol: symbol, Canonical: symbol,
	}); err != nil {
		t.Fatalf("seed symbol map: %v", err)
	}
	posID, err := s.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: oppID, Type: "XASSET_CARRY", Status: "OPEN", Environment: "live",
	})
	if err != nil {
		t.Fatalf("create pos: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 1, Phase: "OPEN", Venue: venueKey, Symbol: symbol,
		Side: "BUY", Qty: qty, Status: "FILLED", FilledQty: &qty, AvgFillPrice: &price,
	}); err != nil {
		t.Fatalf("create leg: %v", err)
	}
	return posID
}

// d405CSStub CS 查询桩（XAUUSD 类 CS=1 简化；canonical 计价币=USD → perLot=price×cs）。
func d405CSStub(venueKey, symbol string) (float64, error) { return 1, nil }

// TestD405_Evaluate_MT_Tiers DSN 锚：free_margin 三档落库 + data_stale + 冷却持久化。
// XAUUSD 名义=qty×price×cs=10×2000×1=20000；日波动≈4.76%→dailyVol≈952 → warn 线 ≈2857/urgent 线 ≈1429。
func TestD405_Evaluate_MT_Tiers(t *testing.T) {
	s := d405Store(t)
	ctx := context.Background()
	venue, vt, sym := "8888001", "mt5", "XAUUSD"
	d405SeedTicks(t, s, venue, sym)
	d405SeedMTLeg(t, s, venue+":"+vt, sym, "XASSET_CARRY:XAUUSD:a:b", 10, 2000)

	g := NewMarginGuard(s, bus.New(), d405CSStub, 30*time.Minute, 6*time.Hour)
	snap := func(free float64) *pgstore.VenueAccountSnapshot {
		return &pgstore.VenueAccountSnapshot{
			Venue: venue, VenueType: vt, EquityRaw: 5000, FreeMarginRaw: free,
			Currency: "USD", FetchedAt: time.Now(),
		}
	}
	readTier := func() *pgstore.VenueMarginStatus {
		r, ok, err := s.GetVenueMarginStatus(ctx, venue, vt)
		if err != nil || !ok {
			t.Fatalf("GetVenueMarginStatus ok=%v err=%v", ok, err)
		}
		return r
	}

	// warn：free=2000 ∈ [1429, 2857)
	if err := g.evaluate(ctx, snap(2000)); err != nil {
		t.Fatalf("evaluate warn: %v", err)
	}
	r := readTier()
	if r.Tier != "warn" || r.CoverageDays == nil || *r.CoverageDays < 1.9 || *r.CoverageDays > 2.3 {
		t.Fatalf("warn tier=%s cov=%v", r.Tier, r.CoverageDays)
	}
	if r.LastAlertAt == nil {
		t.Fatal("warn 首发应落 last_alert_at（冷却持久化锚）")
	}

	// urgent 去抖：第 1 tick 不发（streak=1，且换档清冷却基 → last_alert_at 回 nil）
	if err := g.evaluate(ctx, snap(1000)); err != nil {
		t.Fatalf("evaluate urgent#1: %v", err)
	}
	r = readTier()
	if r.Tier != "urgent" {
		t.Fatalf("tier=%s want urgent", r.Tier)
	}
	if r.LastAlertAt != nil {
		t.Fatal("urgent 首 tick 去抖应不发告警（last_alert_at=nil）")
	}
	// urgent#2：streak=2 → 告警落 last_alert_at
	if err := g.evaluate(ctx, snap(1000)); err != nil {
		t.Fatalf("evaluate urgent#2: %v", err)
	}
	r = readTier()
	if r.LastAlertAt == nil {
		t.Fatal("urgent 连续 2 tick 应发告警落 last_alert_at")
	}
	firstAlert := *r.LastAlertAt
	// 重启后冷却恢复：新 guard（内存 streak 清零）prev=urgent 同档延续 → streak=1 去抖拦
	g2 := NewMarginGuard(s, bus.New(), d405CSStub, 30*time.Minute, 6*time.Hour)
	if err := g2.evaluate(ctx, snap(1000)); err != nil {
		t.Fatalf("evaluate restart: %v", err)
	}
	r2, _, _ := s.GetVenueMarginStatus(ctx, venue, vt)
	if !r2.LastAlertAt.Equal(firstAlert) {
		t.Fatal("重启后 streak=1 去抖应不刷新 last_alert_at（冷却持久化生效）")
	}
}

// TestD405_Evaluate_DataStale 腿 ticks 缺 → data_stale 落库（不保留旧绿）。
func TestD405_Evaluate_DataStale(t *testing.T) {
	s := d405Store(t)
	ctx := context.Background()
	venue, vt, sym := "8888002", "mt5", "XPTUSD"
	// 腿存在但零 ticks → 波动缺 → data_stale
	d405SeedMTLeg(t, s, venue+":"+vt, sym, "XASSET_CARRY:XPTUSD:a:b", 1, 100)
	g := NewMarginGuard(s, bus.New(), d405CSStub, time.Minute, time.Minute)
	sn := &pgstore.VenueAccountSnapshot{
		Venue: venue, VenueType: vt, EquityRaw: 5000, FreeMarginRaw: 9999,
		Currency: "USD", FetchedAt: time.Now(),
	}
	// 先喂一次正常档（无腿匹配？——腿 venue 匹配，ticks 缺 → 即 data_stale）
	if err := g.evaluate(ctx, sn); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	r, ok, err := s.GetVenueMarginStatus(ctx, venue, vt)
	if err != nil || !ok {
		t.Fatalf("GetVenueMarginStatus: ok=%v err=%v", ok, err)
	}
	if r.Tier != "data_stale" {
		t.Fatalf("tier=%s want data_stale（ticks 缺禁陈旧绿）", r.Tier)
	}
}
