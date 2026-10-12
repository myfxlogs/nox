package swapledger

// 锚：CEX perp 腿比对日=昨日（日中部分窗对全日公式误报结构性修复）+
// 公式值持仓时长折算（同日开仓首日同根误报）。
// T1=主锚日中不误报 / T2=昨日完整日对平 / T3=昨日实收真0仍critical / T4=同日开仓折算 /
// T5=多窗日和+比对日断言 / T6=MT 回归（d349 T4 续绿覆盖）。

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/store/pgstore"
)

// d437StubStore 按日键控返回的桩——perp 比对日语义直接可验（记录查询日）。
type d437StubStore struct {
	legs       []pgstore.PositionLeg
	formulaDay map[string]float64 // day(2006-01-02) → 公式值
	incomeDay  map[string]float64 // day → income 日和
	incomeRows map[string]int
	cfDays     []string // HasCashflowForLegDay 收到的 day（比对日断言）
	incDays    []string
	alerts     []string
}

func d437Key(day time.Time) string { return day.Format("2006-01-02") }

func (s *d437StubStore) ListOpenPositionsByType(_ context.Context, _ []string) ([]pgstore.Position, error) {
	return nil, nil
}
func (s *d437StubStore) ListPositionLegs(_ context.Context, _ int64) ([]pgstore.PositionLeg, error) {
	return s.legs, nil
}
func (s *d437StubStore) HasCashflowForLegDay(_ context.Context, _ int64, _ int16, _ string, day time.Time) (bool, error) {
	s.cfDays = append(s.cfDays, d437Key(day))
	return true, nil
}
func (s *d437StubStore) SumCashflowsForDay(_ context.Context, _ int64, _, _ string, day time.Time) (float64, error) {
	return s.formulaDay[d437Key(day)], nil
}
func (s *d437StubStore) SumIncomeForDay(_ context.Context, _, _ string, day time.Time) (float64, int, error) {
	k := d437Key(day)
	s.incDays = append(s.incDays, k)
	return s.incomeDay[k], s.incomeRows[k], nil
}
func (s *d437StubStore) LatestSwapSnapshot(_ context.Context, _ int64, _ string) (pgstore.SwapSnapshot, error) {
	return pgstore.SwapSnapshot{}, errNoRows
}
func (s *d437StubStore) LatestSwapSnapshotBefore(_ context.Context, _ int64, _ string, _ time.Time) (pgstore.SwapSnapshot, error) {
	return pgstore.SwapSnapshot{}, errNoRows
}
func (s *d437StubStore) InsertAlert(_ context.Context, _ *int64, level, msg string) error {
	s.alerts = append(s.alerts, level+": "+msg)
	return nil
}

func d437PerpLeg() []pgstore.PositionLeg {
	return []pgstore.PositionLeg{{LegNo: 1, Phase: "OPEN", Venue: "okx:perp", Symbol: "XAU-USDT-SWAP"}}
}

// TestD437_T1_MidDayNoFalseAlert T1 主锚：pos502 形态——now=当日（日中），perp 腿
// 当日 cashflow+首窗 income（公式 $2.97 vs $1.0313=65% 偏差的误报形态），
// 昨日完整日对平 → 0 告警。红=旧码比对当日 → critical。
func TestD437_T1_MidDayNoFalseAlert(t *testing.T) {
	today := time.Date(2026, 9, 24, 2, 0, 0, 0, time.UTC)
	yday := d437Key(today.AddDate(0, 0, -1))
	tday := d437Key(today)
	st := &d437StubStore{
		legs:       d437PerpLeg(),
		formulaDay: map[string]float64{tday: 2.97, yday: 2.97},
		incomeDay:  map[string]float64{tday: 1.0313, yday: 2.97}, // 当日只结了 1/3 窗；昨日对平
		incomeRows: map[string]int{tday: 1, yday: 3},
	}
	r := NewSwapReconciler(st, 10.0)
	n, err := r.reconcilePosition(context.Background(),
		pgstore.Position{ID: 502, Type: "XASSET_CARRY"}, today)
	if err != nil {
		t.Fatalf("reconcilePosition: %v", err)
	}
	if n != 0 || len(st.alerts) != 0 {
		t.Fatalf("日中应 0 告警（红=对全日公式比首窗 income），got n=%d alerts=%v", n, st.alerts)
	}
}

// TestD437_T2_YesterdayFullDayBalanced T2：昨日公式≈实收日和（差<10%）→ 0 告警；
// 且比对日断言=昨日（cmpDay 传导正确）。
func TestD437_T2_YesterdayFullDayBalanced(t *testing.T) {
	today := time.Date(2026, 9, 24, 2, 0, 0, 0, time.UTC)
	yday := d437Key(today.AddDate(0, 0, -1))
	st := &d437StubStore{
		legs:       d437PerpLeg(),
		formulaDay: map[string]float64{yday: 1.0951},
		incomeDay:  map[string]float64{yday: 1.00},
		incomeRows: map[string]int{yday: 3},
	}
	r := NewSwapReconciler(st, 10.0)
	n, err := r.reconcilePosition(context.Background(),
		pgstore.Position{ID: 492, Type: "XASSET_CARRY"}, today)
	if err != nil {
		t.Fatalf("reconcilePosition: %v", err)
	}
	if n != 0 || len(st.alerts) != 0 {
		t.Fatalf("偏差 8.7%%<10%% 应 0 告警，got %v", st.alerts)
	}
	if len(st.cfDays) != 1 || st.cfDays[0] != yday {
		t.Fatalf("cashflow 预检查日=%v want [%s]（perp 比对日=昨日）", st.cfDays, yday)
	}
	if len(st.incDays) != 1 || st.incDays[0] != yday {
		t.Fatalf("income 查询日=%v want [%s]", st.incDays, yday)
	}
}

// TestD437_T3_YesterdayZeroIncomeAlerts T3：昨日 income 0 行+公式>0 → critical
// （100% 偏差=真 miss 保留检出，不误杀）。
func TestD437_T3_YesterdayZeroIncomeAlerts(t *testing.T) {
	today := time.Date(2026, 9, 24, 2, 0, 0, 0, time.UTC)
	yday := d437Key(today.AddDate(0, 0, -1))
	st := &d437StubStore{
		legs:       d437PerpLeg(),
		formulaDay: map[string]float64{yday: 1.0951},
		incomeDay:  map[string]float64{},
		incomeRows: map[string]int{},
	}
	r := NewSwapReconciler(st, 10.0)
	n, err := r.reconcilePosition(context.Background(),
		pgstore.Position{ID: 492, Type: "XASSET_CARRY"}, today)
	if err != nil {
		t.Fatalf("reconcilePosition: %v", err)
	}
	if n != 1 || len(st.alerts) != 1 {
		t.Fatalf("昨日实收 0 应 critical 真告警（真 miss 检出保留），got n=%d alerts=%v", n, st.alerts)
	}
}

// TestD437_T4_SameDayOpenScaled T4 主锚：昨日 12:00 开仓（heldFrac=0.5），
// 实收=半日窗和 → 折算后对平 0 告警。红=不折算 50%+ 误报。
func TestD437_T4_SameDayOpenScaled(t *testing.T) {
	today := time.Date(2026, 9, 24, 2, 0, 0, 0, time.UTC)
	yday := today.AddDate(0, 0, -1)
	ydayKey := d437Key(yday)
	opened := time.Date(yday.Year(), yday.Month(), yday.Day(), 12, 0, 0, 0, time.UTC)
	tdayKey := d437Key(today)
	st := &d437StubStore{
		legs:       d437PerpLeg(),
		formulaDay: map[string]float64{ydayKey: 2.97, tdayKey: 2.97},   // 全日投影
		incomeDay:  map[string]float64{ydayKey: 1.485, tdayKey: 1.485}, // 半日实收
		incomeRows: map[string]int{ydayKey: 2, tdayKey: 2},
	}
	r := NewSwapReconciler(st, 10.0)
	n, err := r.reconcilePosition(context.Background(),
		pgstore.Position{ID: 502, Type: "XASSET_CARRY", OpenedAt: &opened}, today)
	if err != nil {
		t.Fatalf("reconcilePosition: %v", err)
	}
	if n != 0 || len(st.alerts) != 0 {
		t.Fatalf("同日开仓半日持仓应折算对平 0 告警（红=未折算 50%% 误报），got %v", st.alerts)
	}
}

// TestD437_T5_MultiWindowSum T5：昨日 3 行 income 跨窗 → 比对用日和（3 窗合 $2.97 vs
// 公式 $2.97 → 0 告警；若误用首窗 $1.03 则 65% 误报）。
func TestD437_T5_MultiWindowSum(t *testing.T) {
	today := time.Date(2026, 9, 24, 2, 0, 0, 0, time.UTC)
	yday := d437Key(today.AddDate(0, 0, -1))
	st := &d437StubStore{
		legs:       d437PerpLeg(),
		formulaDay: map[string]float64{yday: 2.97},
		incomeDay:  map[string]float64{yday: 2.97}, // SumIncomeForDay 日和（3 行合）
		incomeRows: map[string]int{yday: 3},
	}
	r := NewSwapReconciler(st, 10.0)
	n, err := r.reconcilePosition(context.Background(),
		pgstore.Position{ID: 502, Type: "XASSET_CARRY"}, today)
	if err != nil {
		t.Fatalf("reconcilePosition: %v", err)
	}
	if n != 0 || len(st.alerts) != 0 {
		t.Fatalf("3 窗日和应对平 0 告警，got %v", st.alerts)
	}
	if len(st.incDays) != 1 || st.incDays[0] != yday {
		t.Fatalf("income 应查昨日日和，got days=%v", st.incDays)
	}
}

// SwapReconcileStore 接口补法（窗径桩——本测试走 notional 缺省回退路径不触达）。
func (s *d437StubStore) FactsSince(context.Context, string, string, string, time.Time) ([]fact.Fact, error) {
	return nil, nil
}
func (s *d437StubStore) GetOpportunity(context.Context, string) (*pgstore.Opportunity, error) {
	return nil, nil
}
func (s *d437StubStore) ListSymbolMappings(context.Context, string) ([]pgstore.SymbolMapping, error) {
	return nil, nil
}
