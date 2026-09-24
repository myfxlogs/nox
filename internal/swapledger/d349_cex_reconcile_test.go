package swapledger

// D-349 锚：CEX perp 腿对账分流 income 实收（binance Order.Swap 恒 0 结构性假报修复）。
// T1=492 场景红锚（公式 1.0951+income 0 行+未过窗→不告警；分流摘除回快照差→100% 假报=红）。
// T2=income 实收 1.00 偏差 8.7% 不告警。T3=过窗+0 实收→真告警保留。
// T4=MT 腿回归（快照差路径不动）。

import (
	"context"
	"errors"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// d349StubStore SwapReconcileStore 桩——公式值/income/快照/告警全可控。
type d349StubStore struct {
	formulaUSD float64
	incomeUSD  float64
	incomeRows int
	todaySnap  pgstore.SwapSnapshot
	ydaySnap   pgstore.SwapSnapshot
	alerts     []string
	incomeSym  string // 记录 SumIncomeForDay 收到的 symbol
	incomeVen  string
}

func (s *d349StubStore) ListOpenPositionsByType(_ context.Context, _ []string) ([]pgstore.Position, error) {
	return nil, nil
}
func (s *d349StubStore) ListPositionLegs(_ context.Context, _ int64) ([]pgstore.PositionLeg, error) {
	return nil, nil
}
func (s *d349StubStore) HasCashflowForLegDay(_ context.Context, _ int64, _ int16, _ string, _ time.Time) (bool, error) {
	return true, nil
}
func (s *d349StubStore) SumCashflowsForDay(_ context.Context, _ int64, _, _ string, _ time.Time) (float64, error) {
	return s.formulaUSD, nil
}
func (s *d349StubStore) SumIncomeForDay(_ context.Context, venue, symbol string, _ time.Time) (float64, int, error) {
	s.incomeVen, s.incomeSym = venue, symbol
	return s.incomeUSD, s.incomeRows, nil
}
func (s *d349StubStore) LatestSwapSnapshot(_ context.Context, _ int64, _ string) (pgstore.SwapSnapshot, error) {
	if s.todaySnap.ID == 0 {
		return pgstore.SwapSnapshot{}, errNoRows
	}
	return s.todaySnap, nil
}
func (s *d349StubStore) LatestSwapSnapshotBefore(_ context.Context, _ int64, _ string, _ time.Time) (pgstore.SwapSnapshot, error) {
	if s.ydaySnap.ID == 0 {
		return pgstore.SwapSnapshot{}, errNoRows
	}
	return s.ydaySnap, nil
}
func (s *d349StubStore) InsertAlert(_ context.Context, _ *int64, level, msg string) error {
	s.alerts = append(s.alerts, level+": "+msg)
	return nil
}

var errNoRows = errors.New("no rows")

// TestD349_CEXLegIncomeReconcile T1：pos 492 场景——公式 1.0951 + income 0 行 + 未过结算窗 → 不告警。
// 分流摘除（回快照差）时：快照 0/0 → 实际增量 0 vs 公式 1.0951 = 100% → 告警=红锚。
func TestD349_CEXLegIncomeReconcile(t *testing.T) {
	day := time.Now().UTC()
	// 快照 0/0 双行=492 实查形态（binance Order.Swap 恒 0）——分流摘除时快照差=0 恰产 100% 假报。
	st := &d349StubStore{formulaUSD: 1.0951, incomeRows: 0,
		ydaySnap:  pgstore.SwapSnapshot{ID: 1, SwapUSD: 0, Ts: day.Add(-2 * time.Hour)},
		todaySnap: pgstore.SwapSnapshot{ID: 2, SwapUSD: 0, Ts: day}}
	r := NewSwapReconciler(st, 10.0)
	alerted, err := r.reconcileLegWithStore(context.Background(), pgstore.Position{ID: 492, Type: "XASSET_CARRY"}, "binance:perp", "XAUUSDT", day, day)
	if err != nil {
		t.Fatalf("reconcileLegWithStore: %v", err)
	}
	if alerted || len(st.alerts) != 0 {
		t.Fatalf("income 0 行+未过窗应 skip 不告警（现假报 %v）", st.alerts)
	}
	if st.incomeVen != "binance" || st.incomeSym != "XAUUSDT" {
		t.Errorf("SumIncomeForDay 参数 venue=%q symbol=%q, want binance/XAUUSDT（剥 :perp+腿原生符号）", st.incomeVen, st.incomeSym)
	}
}

// TestD349_CEXLegIncomeWithinThreshold T2：income 实收 1.00 vs 公式 1.0951 → 偏差 8.7%<10% 不告警。
func TestD349_CEXLegIncomeWithinThreshold(t *testing.T) {
	day := time.Now().UTC()
	st := &d349StubStore{formulaUSD: 1.0951, incomeUSD: 1.00, incomeRows: 1}
	r := NewSwapReconciler(st, 10.0)
	alerted, err := r.reconcileLegWithStore(context.Background(), pgstore.Position{ID: 492, Type: "XASSET_CARRY"}, "binance:perp", "XAUUSDT", day, day)
	if err != nil {
		t.Fatalf("reconcileLegWithStore: %v", err)
	}
	if alerted {
		t.Fatalf("偏差 8.7%% 应不告警（got alert %v）", st.alerts)
	}
}

// TestD349_CEXLegPastWindowZeroIncome T3：过结算窗+income 仍 0 行 vs 正公式 → 真告警（实收 0 是真信息）。
func TestD349_CEXLegPastWindowZeroIncome(t *testing.T) {
	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	now := time.Now().UTC()
	st := &d349StubStore{formulaUSD: 1.0951, incomeRows: 0}
	r := NewSwapReconciler(st, 10.0)
	alerted, err := r.reconcileLegWithStore(context.Background(), pgstore.Position{ID: 492, Type: "XASSET_CARRY"}, "binance:perp", "XAUUSDT", yesterday, now)
	if err != nil {
		t.Fatalf("reconcileLegWithStore: %v", err)
	}
	if !alerted || len(st.alerts) != 1 {
		t.Fatalf("过窗+0 实收应真告警（100%% 偏差保留），got alerted=%v alerts=%v", alerted, st.alerts)
	}
}

// TestD349_MTLegSnapshotPathUnchanged T4：MT 腿回归——快照差路径不动（:perp 后缀分流不影响）。
// 快照 −0.55 差 vs 公式 −0.55 → 0 偏差不告警；公式 −0.55 vs 实收 0 → 100% 告警（原 Exness 场景）。
func TestD349_MTLegSnapshotPathUnchanged(t *testing.T) {
	day := time.Now().UTC()
	y := pgstore.SwapSnapshot{ID: 1, SwapUSD: 0, Ts: day.Add(-2 * time.Hour)}
	td := pgstore.SwapSnapshot{ID: 2, SwapUSD: -0.55, Ts: day}
	st := &d349StubStore{formulaUSD: -0.55, todaySnap: td, ydaySnap: y}
	r := NewSwapReconciler(st, 10.0)
	alerted, err := r.reconcileLegWithStore(context.Background(), pgstore.Position{ID: 492, Type: "XASSET_CARRY"}, "277842155:mt5", "XAUUSD247m", day, day)
	if err != nil {
		t.Fatalf("reconcileLegWithStore: %v", err)
	}
	if alerted {
		t.Fatalf("MT 腿快照差 −0.55=公式 −0.55 应不告警（got %v）", st.alerts)
	}
	// 偏差分支：快照差 0 vs 公式 −0.55 → 100% 告警（快照差路径原样保留）
	st2 := &d349StubStore{formulaUSD: -0.55, todaySnap: pgstore.SwapSnapshot{ID: 3, SwapUSD: 0, Ts: day}, ydaySnap: y}
	r2 := NewSwapReconciler(st2, 10.0)
	alerted2, err := r2.reconcileLegWithStore(context.Background(), pgstore.Position{ID: 492, Type: "XASSET_CARRY"}, "277842155:mt5", "XAUUSD247m", day, day)
	if err != nil {
		t.Fatalf("reconcileLegWithStore: %v", err)
	}
	if !alerted2 {
		t.Fatal("MT 腿快照差 0 vs 公式 −0.55 应告警（快照差路径偏差分支保留）")
	}
}
