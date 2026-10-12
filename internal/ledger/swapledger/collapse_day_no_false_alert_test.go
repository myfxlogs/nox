// d161_windows_test.go —— D-161 锚：塌缩率日内序列→逐窗期望≈实收零误报（A 主径）；
// facts 窗覆盖不足→回退日频旧径（C 兜底）；稳态日→比对不变；funding 零覆盖→skip。
package swapledger

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/data/fact"
	"arb/internal/store/pgstore"
)

// d161Income income_events 桩行——时刻独立构造（复审返修：结算收入 ts=结算点+延迟，
// 日和按时刻归窗，不做日级总键直接拍平）。
type d161Income struct {
	ts  time.Time
	usd float64
}

// d161StubStore 全接口桩——facts 时序/income 事件/公式值/cashflow 幂等全可控。
type d161StubStore struct {
	legs      []pgstore.PositionLeg
	formula   float64 // SumCashflowsForDay 返回值（旧径）
	income    []d161Income
	facts     map[string][]fact.Fact // "funding" | "funding_interval"
	factsErr  error
	alerts    []string
	cfQueried bool // SumCashflowsForDay 被调=走了日频旧径
}

func (s *d161StubStore) ListOpenPositionsByType(context.Context, []string) ([]pgstore.Position, error) {
	return nil, nil
}
func (s *d161StubStore) ListPositionLegs(context.Context, int64) ([]pgstore.PositionLeg, error) {
	return s.legs, nil
}
func (s *d161StubStore) HasCashflowForLegDay(context.Context, int64, int16, string, time.Time) (bool, error) {
	return true, nil
}
func (s *d161StubStore) SumCashflowsForDay(context.Context, int64, string, string, time.Time) (float64, error) {
	s.cfQueried = true
	return s.formula, nil
}
func (s *d161StubStore) SumIncomeForDay(_ context.Context, _, _ string, day time.Time) (float64, int, error) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	var sum float64
	var rows int
	for _, e := range s.income {
		if !e.ts.Before(start) && e.ts.Before(end) {
			sum += e.usd
			rows++
		}
	}
	return sum, rows, nil
}
func (s *d161StubStore) LatestSwapSnapshot(context.Context, int64, string) (pgstore.SwapSnapshot, error) {
	return pgstore.SwapSnapshot{}, errNoRows
}
func (s *d161StubStore) LatestSwapSnapshotBefore(context.Context, int64, string, time.Time) (pgstore.SwapSnapshot, error) {
	return pgstore.SwapSnapshot{}, errNoRows
}
func (s *d161StubStore) InsertAlert(_ context.Context, _ *int64, level, msg string) error {
	s.alerts = append(s.alerts, level+": "+msg)
	return nil
}
func (s *d161StubStore) FactsSince(_ context.Context, kind, _, _ string, since time.Time) ([]fact.Fact, error) {
	return s.facts[kind], s.factsErr
}
func (s *d161StubStore) GetOpportunity(context.Context, string) (*pgstore.Opportunity, error) {
	return nil, nil
}
func (s *d161StubStore) ListSymbolMappings(context.Context, string) ([]pgstore.SymbolMapping, error) {
	return nil, nil
}

// d161Notional 固定 10000 USD/腿名义（usdPerLot×qty=1×10000 同效）。
type d161Notional struct{ perLot float64 }

func (n d161Notional) USDPerLotForLeg(string, string, string) (float64, bool) {
	return n.perLot, n.perLot > 0
}

// d161IncomeAt 结算点后 4s 落行（生产形态：okx 收入 ts=结算点+秒级延迟）。
func d161IncomeAt(settle time.Time, usd float64) d161Income {
	return d161Income{ts: settle.Add(4 * time.Second), usd: usd}
}

func d161Leg() []pgstore.PositionLeg {
	return []pgstore.PositionLeg{{LegNo: 1, Phase: "OPEN", Venue: "okx:perp", Symbol: "XAU-USDT-SWAP", Side: "SELL", Qty: 1}}
}

// d161Pos oppID 段 canonical=XAUUSDT（facts/income 键同形）。
func d161Pos() pgstore.Position {
	return pgstore.Position{ID: 510, Type: "XASSET_CARRY", OpportunityID: "XASSET:XAUUSDT:okx"}
}

// d161Facts 塌缩率序列：08:00 界前 2.0%、之后 0.5%（ivl=8h → 三窗率 2/0.5/0.5）。
func d161Facts(day time.Time) map[string][]fact.Fact {
	d0 := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	mk := func(ts time.Time, v float64) fact.Fact {
		return fact.Fact{Kind: "funding", Venue: "okx", Symbol: "XAUUSDT", Value: v, Ts: ts}
	}
	return map[string][]fact.Fact{
		"funding":          {mk(d0.Add(-time.Hour), 2.0), mk(d0.Add(8*time.Hour), 0.5)},
		"funding_interval": {{Kind: "funding_interval", Venue: "okx", Symbol: "XAUUSDT", Value: 28800, Ts: d0.Add(-time.Hour)}},
	}
}

// TestD161_CollapseDay_NoFalseAlert 主锚：#510 形态——首日率塌缩（2%→0.5%），
// 逐窗期望=Σ(2,0.5,0.5)%/365×8h/24h×10000≈0.274 对实收≈0.274 → 0 告警；
// 红=日频点估计 2%→0.548 vs 0.274=50% critical。income 按结算时刻三事件构造。
func TestD161_CollapseDay_NoFalseAlert(t *testing.T) {
	yday := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	today := yday.AddDate(0, 0, 1)
	win := func(h int) float64 { return [3]float64{2.0, 0.5, 0.5}[h] / 100 / 365 / 3 * 10000 }
	st := &d161StubStore{
		legs: d161Leg(),
		income: []d161Income{
			d161IncomeAt(yday, win(0)),                   // S=00:00（前日 16-24 窗率 2%）
			d161IncomeAt(yday.Add(8*time.Hour), win(1)),  // S=08:00（恰界 facts 0.5%）
			d161IncomeAt(yday.Add(16*time.Hour), win(2)), // S=16:00
		},
		facts: d161Facts(yday),
	}
	r := NewSwapReconciler(st, 10.0)
	r.SetNotional(d161Notional{10000})
	n, err := r.reconcilePosition(context.Background(), d161Pos(), today)
	if err != nil {
		t.Fatalf("reconcilePosition: %v", err)
	}
	if n != 0 || len(st.alerts) != 0 {
		t.Fatalf("逐窗期望≈实收应 0 告警（红=日频点估计误报），got n=%d alerts=%v", n, st.alerts)
	}
	if st.cfQueried {
		t.Error("窗径不应消费仓级日频公式值（SumCashflowsForDay 被调=走了旧径）")
	}
}

// TestD161_TrueMismatch_StillCritical 反向锚：逐窗期望 0.274 vs 实收 0（真错收）→
// 100% 偏差 critical 照旧（fail-closed 不因口径升精度漏真偏差）。
func TestD161_TrueMismatch_StillCritical(t *testing.T) {
	yday := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	today := yday.AddDate(0, 0, 1)
	st := &d161StubStore{
		legs:   d161Leg(),
		income: []d161Income{d161IncomeAt(yday, 0)}, // 有行但金额 0=实收 0
		facts:  d161Facts(yday),
	}
	r := NewSwapReconciler(st, 10.0)
	r.SetNotional(d161Notional{10000})
	n, err := r.reconcilePosition(context.Background(), d161Pos(), today)
	if err != nil {
		t.Fatalf("reconcilePosition: %v", err)
	}
	if n != 1 || len(st.alerts) != 1 || !strings.Contains(st.alerts[0], "critical") {
		t.Fatalf("实收 0 真偏差应 critical 照旧，got n=%d alerts=%v", n, st.alerts)
	}
	if !strings.Contains(st.alerts[0], "逐窗口径") {
		t.Errorf("告警应带逐窗口径标注，got %s", st.alerts[0])
	}
}

// TestD161_Fallback_NoInterval C 兜底：funding_interval 缺 → 全组回退日频旧径
// （SumCashflowsForDay 被调+旧告警径可用——真偏差仍检出）。
func TestD161_Fallback_NoInterval(t *testing.T) {
	yday := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	today := yday.AddDate(0, 0, 1)
	st := &d161StubStore{
		legs:  d161Leg(),
		facts: map[string][]fact.Fact{"funding": d161Facts(yday)["funding"]}, // interval 缺
	}
	r := NewSwapReconciler(st, 10.0)
	r.SetNotional(d161Notional{10000})
	if _, err := r.reconcilePosition(context.Background(), d161Pos(), today); err != nil {
		t.Fatalf("reconcilePosition: %v", err)
	}
	if !st.cfQueried {
		t.Error("interval 缺应回退日频旧径（SumCashflowsForDay 未被调=错径）")
	}
}

// TestD161_Fallback_NoNotional 无名义源（未 SetNotional）→ 旧径照旧（向后兼容锚——
// d349/d437 全绿已覆盖，本锚钉接线缺口语义）。
func TestD161_Fallback_NoNotional(t *testing.T) {
	yday := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	today := yday.AddDate(0, 0, 1)
	st := &d161StubStore{legs: d161Leg(), facts: d161Facts(yday)}
	r := NewSwapReconciler(st, 10.0) // 不注入 notional
	if _, err := r.reconcilePosition(context.Background(), d161Pos(), today); err != nil {
		t.Fatalf("reconcilePosition: %v", err)
	}
	if !st.cfQueried {
		t.Error("无名义源应回退日频旧径")
	}
}

// TestD161_ZeroFacts_Skip funding 零覆盖 → skip（无告警无旧径——率史缺席不猜）。
func TestD161_ZeroFacts_Skip(t *testing.T) {
	yday := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	today := yday.AddDate(0, 0, 1)
	st := &d161StubStore{
		legs:  d161Leg(),
		facts: map[string][]fact.Fact{"funding_interval": d161Facts(yday)["funding_interval"]},
	}
	r := NewSwapReconciler(st, 10.0)
	r.SetNotional(d161Notional{10000})
	n, err := r.reconcilePosition(context.Background(), d161Pos(), today)
	if err != nil {
		t.Fatalf("reconcilePosition: %v", err)
	}
	if n != 0 || len(st.alerts) != 0 || st.cfQueried {
		t.Fatalf("funding 零覆盖应 skip，got n=%d alerts=%v cfQueried=%v", n, st.alerts, st.cfQueried)
	}
}

// TestD161_SameDayOpen_WindowsOnlyHeld 日内开仓折窗：16:00 开仓 → 只计 24:00 窗
// （前两窗 openedAt≥结算界不收——天然含 heldFrac）。
// TestD161_SameDayOpen_WindowsOnlyHeld 日内开仓折窗：15:59 开仓 → 只计 S=16:00
// 结算点（前两界 ≤openedAt 不收）；income 按该结算时刻独立构造。
func TestD161_SameDayOpen_WindowsOnlyHeld(t *testing.T) {
	yday := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	today := yday.AddDate(0, 0, 1)
	opened := yday.Add(15*time.Hour + 59*time.Minute)
	pos := d161Pos()
	pos.OpenedAt = &opened
	exp := 0.5 / 100 / 365 / 3 * 10000 // 仅 S=16:00 窗 rate 0.5%
	st := &d161StubStore{
		legs:   d161Leg(),
		income: []d161Income{d161IncomeAt(yday.Add(16*time.Hour), exp)},
		facts:  d161Facts(yday),
	}
	r := NewSwapReconciler(st, 10.0)
	r.SetNotional(d161Notional{10000})
	n, err := r.reconcilePosition(context.Background(), pos, today)
	if err != nil {
		t.Fatalf("reconcilePosition: %v", err)
	}
	if n != 0 || len(st.alerts) != 0 {
		t.Fatalf("日内开仓只计后窗应对平 0 告警，got n=%d alerts=%v", n, st.alerts)
	}
}
