// Package reconcile 对账自动收尾单测（D-073+）。
// 覆盖：查证命中→收尾流水（含另一腿自动平）、查证未命中→仍 critical、
// OPENING 僵尸转 FAILED、幂等（已 CLOSED 不再处置）。
package reconcile

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// —— fakeStore：实现 reconcile.Store 接口 ——

type fakeStore struct {
	positions   map[int64]*pgstore.Position
	legs        map[int64][]*pgstore.PositionLeg
	alerts      []fakeAlert
	closedIDs   []int64
	failedIDs   []int64
	closeCalls  int
	statusCalls map[int64]string
}

type fakeAlert struct {
	posID *int64
	level string
	msg   string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		positions:   make(map[int64]*pgstore.Position),
		legs:        make(map[int64][]*pgstore.PositionLeg),
		statusCalls: make(map[int64]string),
	}
}

func (f *fakeStore) ListPositions(ctx context.Context, status string, limit, offset int) ([]pgstore.Position, error) {
	var out []pgstore.Position
	for _, p := range f.positions {
		if p.Status == status {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (f *fakeStore) ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error) {
	var out []pgstore.PositionLeg
	for _, l := range f.legs[posID] {
		out = append(out, *l)
	}
	return out, nil
}

func (f *fakeStore) CreatePositionLeg(ctx context.Context, leg *pgstore.PositionLeg) error {
	f.legs[leg.PositionID] = append(f.legs[leg.PositionID], leg)
	return nil
}

func (f *fakeStore) ClosePosition(ctx context.Context, id int64, realizedPnl float64) error {
	f.closeCalls++
	f.closedIDs = append(f.closedIDs, id)
	if p, ok := f.positions[id]; ok {
		p.Status = "CLOSED"
		p.RealizedPnl = &realizedPnl
	}
	return nil
}

func (f *fakeStore) UpdatePositionStatus(ctx context.Context, id int64, status string) error {
	f.statusCalls[id] = status
	if p, ok := f.positions[id]; ok {
		p.Status = status
	}
	if status == "FAILED" {
		f.failedIDs = append(f.failedIDs, id)
	}
	return nil
}

func (f *fakeStore) InsertAlert(ctx context.Context, posID *int64, level, msg string) error {
	f.alerts = append(f.alerts, fakeAlert{posID: posID, level: level, msg: msg})
	return nil
}

// —— fakeCloser：实现 Closer 接口 ——

type fakeCloser struct {
	calls     []int64
	failOnPos int64 // >0 时该 posID 返回 error
}

func (c *fakeCloser) AutoClosePosition(ctx context.Context, posID int64) error {
	c.calls = append(c.calls, posID)
	if c.failOnPos == posID {
		return fmt.Errorf("模拟平仓失败")
	}
	return nil
}

// —— fakeHistoryQuerier：实现 OrderHistoryQuerier 接口 ——

type fakeHistoryQuerier struct {
	venueKey string
	results  map[string]*HistoryOrder // ticket → result（nil = 未命中）
	errOn    string                   // 该 ticket 返回 error
}

func (q *fakeHistoryQuerier) VenueKey() string { return q.venueKey }

func (q *fakeHistoryQuerier) QueryOrderHistory(ctx context.Context, ticket string, fromDays int) (*HistoryOrder, error) {
	if q.errOn == ticket {
		return nil, fmt.Errorf("模拟查证失败")
	}
	if h, ok := q.results[ticket]; ok {
		return h, nil
	}
	return nil, nil // 未命中
}

// —— fakeStatUpdater：实现 StatUpdater 接口 ——

type fakeStatUpdater struct {
	calls []statCall
}

type statCall struct {
	posType string
	pnl     float64
}

func (s *fakeStatUpdater) UpdateStrategyStat(ctx context.Context, posType string, pnl float64) error {
	s.calls = append(s.calls, statCall{posType, pnl})
	return nil
}

// —— 辅助构造 ——

func ptrFloat(v float64) *float64    { return &v }
func ptrTime(t time.Time) *time.Time { return &t }

func mkPosition(id int64, status, posType string, openedAt time.Time) *pgstore.Position {
	return &pgstore.Position{
		ID:       id,
		Status:   status,
		Type:     posType,
		OpenedAt: ptrTime(openedAt),
	}
}

func mkOpenLeg(posID int64, legNo int16, venue, ticket, side, symbol string, qty float64) *pgstore.PositionLeg {
	return &pgstore.PositionLeg{
		PositionID: posID,
		LegNo:      legNo,
		Phase:      "OPEN",
		Venue:      venue,
		OrderID:    ticket,
		Side:       side,
		Symbol:     symbol,
		Qty:        qty,
		Status:     "FILLED",
	}
}

func hasAlert(f *fakeStore, level, substr string) bool {
	for _, a := range f.alerts {
		if a.level == level && contains(a.msg, substr) {
			return true
		}
	}
	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// —— 测试用例 ——

// TestAutoClose_HitAndCloseOtherLeg 查证命中→收尾流水（含另一腿自动平）。
// 场景：position #100 双腿，ticket=A 已在 venue 平掉（历史单命中），ticket=B 仍 OPEN。
// 期望：回填 A 的 CLOSE leg + 调 closer 平仓（AutoClosePosition）+ position CLOSED + warn。
func TestAutoClose_HitAndCloseOtherLeg(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-2 * time.Hour)
	fs.positions[100] = mkPosition(100, "OPEN", "CROSS_VENUE", opened)
	fs.legs[100] = []*pgstore.PositionLeg{
		mkOpenLeg(100, 0, "52993526:mt5", "A", "BUY", "AUDJPY", 1.0),
		mkOpenLeg(100, 1, "277842155:mt5", "B", "SELL", "AUDJPY", 1.0),
	}

	fc := &fakeCloser{}
	fsu := &fakeStatUpdater{}
	r := &Reconciler{
		store:       fs,
		closer:      fc,
		statUpdater: fsu,
	}

	hist := &HistoryOrder{Ticket: "A", ClosePrice: 100.5, Profit: 10.0, Swap: 0.5, Commission: 1.0, Symbol: "AUDJPY"}
	goneLeg := *mkOpenLeg(100, 0, "52993526:mt5", "A", "BUY", "AUDJPY", 1.0)

	if err := r.autoClose(context.Background(), fs.positions[100], goneLeg, hist); err != nil {
		t.Fatalf("autoClose: %v", err)
	}

	// 1. 回填了 CLOSE leg
	closeLegs := 0
	for _, l := range fs.legs[100] {
		if l.Phase == "CLOSE" {
			closeLegs++
		}
	}
	if closeLegs != 1 {
		t.Errorf("CLOSE leg 数=%d，期望 1", closeLegs)
	}

	// 2. 调了 closer（另一腿自动平）
	if len(fc.calls) != 1 || fc.calls[0] != 100 {
		t.Errorf("closer 调用=%v，期望 [100]", fc.calls)
	}

	// 3. position 置 CLOSED（AutoClosePosition 内部调 ClosePosition，但 fakeCloser 不调 store）
	//    fakeCloser 只记录调用，不实际改状态——autoClose 在 otherOpen=true 时不手动改状态
	//    所以这里验证 closer 被调即可（真实 pipeline.ClosePosition 会调 store.ClosePosition）

	// 4. warn 告警
	if !hasAlert(fs, "warn", "已自动对账收尾 position #100") {
		t.Errorf("缺少 warn 告警，alerts=%v", fs.alerts)
	}

	// 5. 统计更新
	if len(fsu.calls) != 1 || fsu.calls[0].posType != "CROSS_VENUE" {
		t.Errorf("statUpdater 调用=%v，期望 CROSS_VENUE", fsu.calls)
	}
}

// TestRun_HistoryMiss_StillCritical 查证未命中→仍 critical。
// 场景：position #200 单腿 ticket=X，venue 无，历史单也无。
// 期望：critical 告警「真失联」，不调 closer，position 仍 OPEN。
func TestRun_HistoryMiss_StillCritical(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-1 * time.Hour)
	fs.positions[200] = mkPosition(200, "OPEN", "CROSS_VENUE", opened)
	fs.legs[200] = []*pgstore.PositionLeg{
		mkOpenLeg(200, 0, "52993526:mt5", "X", "BUY", "EURUSD", 1.0),
	}

	fc := &fakeCloser{}
	hq := &fakeHistoryQuerier{venueKey: "52993526:mt5", results: map[string]*HistoryOrder{}} // X 未命中
	r := &Reconciler{store: fs, closer: fc}
	r.SetOrderHistoryQueriers(hq)

	mismatches, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mismatches != 1 {
		t.Errorf("mismatches=%d，期望 1", mismatches)
	}
	if !hasAlert(fs, "critical", "真失联") {
		t.Errorf("缺少 critical「真失联」告警，alerts=%v", fs.alerts)
	}
	if len(fc.calls) != 0 {
		t.Errorf("不应调 closer，calls=%v", fc.calls)
	}
	if fs.positions[200].Status != "OPEN" {
		t.Errorf("position 应仍 OPEN，实际 %s", fs.positions[200].Status)
	}
}

// TestRun_HistoryHit_AutoClose 查证命中→Run 触发自动收尾。
// 场景：position #300 单腿 ticket=Y，venue 无，历史单命中。
// 期望：autoClose 收尾 + warn（非 critical）+ position CLOSED。
func TestRun_HistoryHit_AutoClose(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-1 * time.Hour)
	fs.positions[300] = mkPosition(300, "OPEN", "CROSS_VENUE", opened)
	fs.legs[300] = []*pgstore.PositionLeg{
		mkOpenLeg(300, 0, "52993526:mt5", "Y", "BUY", "GBPUSD", 1.0),
	}

	fc := &fakeCloser{}
	fsu := &fakeStatUpdater{}
	hist := &HistoryOrder{Ticket: "Y", ClosePrice: 1.3, Profit: 5.0, Swap: 0, Commission: 0, Symbol: "GBPUSD"}
	hq := &fakeHistoryQuerier{venueKey: "52993526:mt5", results: map[string]*HistoryOrder{"Y": hist}}
	r := &Reconciler{store: fs, closer: fc, statUpdater: fsu}
	r.SetOrderHistoryQueriers(hq)

	mismatches, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 收尾成功不计 mismatches
	if mismatches != 0 {
		t.Errorf("收尾成功 mismatches=%d，期望 0", mismatches)
	}
	if !hasAlert(fs, "warn", "已自动对账收尾 position #300") {
		t.Errorf("缺少 warn 告警，alerts=%v", fs.alerts)
	}
	// 单腿无另一 OPEN 腿 → 手动调 ClosePosition
	if fs.closeCalls != 1 {
		t.Errorf("ClosePosition 调用次数=%d，期望 1", fs.closeCalls)
	}
}

// TestRun_OPENINGZombie_ToFailed OPENING 僵尸（超 24h）→ FAILED + warn。
func TestRun_OPENINGZombie_ToFailed(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-25 * time.Hour) // 超 24h
	fs.positions[400] = mkPosition(400, "OPENING", "CROSS_VENUE", opened)
	fs.legs[400] = []*pgstore.PositionLeg{} // OPENING 无有效腿

	r := &Reconciler{store: fs}

	_, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fs.positions[400].Status != "FAILED" {
		t.Errorf("position 应 FAILED，实际 %s", fs.positions[400].Status)
	}
	if !hasAlert(fs, "warn", "OPENING 僵尸自动归档 position #400") {
		t.Errorf("缺少 warn 告警，alerts=%v", fs.alerts)
	}
}

// TestRun_OPENINGNotZombie_Skip OPENING 未超 24h → 不处置。
func TestRun_OPENINGNotZombie_Skip(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-12 * time.Hour) // 未超 24h
	fs.positions[500] = mkPosition(500, "OPENING", "CROSS_VENUE", opened)

	r := &Reconciler{store: fs}

	_, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fs.positions[500].Status != "OPENING" {
		t.Errorf("未超 24h 不应处置，实际 %s", fs.positions[500].Status)
	}
}

// TestRun_Idempotent_ClosedNotProcessed 幂等：已 CLOSED position 不在 OPEN 列表 → 不处置。
func TestRun_Idempotent_ClosedNotProcessed(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-1 * time.Hour)
	fs.positions[600] = mkPosition(600, "CLOSED", "CROSS_VENUE", opened) // 已 CLOSED
	fs.legs[600] = []*pgstore.PositionLeg{
		mkOpenLeg(600, 0, "52993526:mt5", "Z", "BUY", "USDJPY", 1.0),
	}

	fc := &fakeCloser{}
	hq := &fakeHistoryQuerier{venueKey: "52993526:mt5"}
	r := &Reconciler{store: fs, closer: fc}
	r.SetOrderHistoryQueriers(hq)

	mismatches, _ := r.Run(context.Background())
	// CLOSED 不在 ListPositions("OPEN") 结果中 → 不进入比对 → 0 mismatches
	if mismatches != 0 {
		t.Errorf("已 CLOSED 不应处置，mismatches=%d", mismatches)
	}
	if len(fc.calls) != 0 {
		t.Errorf("已 CLOSED 不应调 closer，calls=%v", fc.calls)
	}
}

// TestRun_HistoryQueryError_Critical 查证失败→critical（fail-closed）。
func TestRun_HistoryQueryError_Critical(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-1 * time.Hour)
	fs.positions[700] = mkPosition(700, "OPEN", "CROSS_VENUE", opened)
	fs.legs[700] = []*pgstore.PositionLeg{
		mkOpenLeg(700, 0, "52993526:mt5", "W", "BUY", "AUDUSD", 1.0),
	}

	fc := &fakeCloser{}
	hq := &fakeHistoryQuerier{venueKey: "52993526:mt5", errOn: "W"}
	r := &Reconciler{store: fs, closer: fc}
	r.SetOrderHistoryQueriers(hq)

	mismatches, _ := r.Run(context.Background())
	if mismatches != 1 {
		t.Errorf("查证失败 mismatches=%d，期望 1", mismatches)
	}
	if !hasAlert(fs, "critical", "对账查证失败") {
		t.Errorf("缺少 critical「查证失败」告警，alerts=%v", fs.alerts)
	}
	if len(fc.calls) != 0 {
		t.Errorf("查证失败不应调 closer，calls=%v", fc.calls)
	}
}

// TestRun_NoHistoryQuerier_Critical 无查证器→维持原 critical 语义。
func TestRun_NoHistoryQuerier_Critical(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-1 * time.Hour)
	fs.positions[800] = mkPosition(800, "OPEN", "CROSS_VENUE", opened)
	fs.legs[800] = []*pgstore.PositionLeg{
		mkOpenLeg(800, 0, "okx:spot", "V", "BUY", "BTCUSDT", 0.1), // CEX 无历史单查证器
	}

	r := &Reconciler{store: fs} // 未注入 historyQueriers

	mismatches, _ := r.Run(context.Background())
	if mismatches != 1 {
		t.Errorf("无查证器 mismatches=%d，期望 1", mismatches)
	}
	if !hasAlert(fs, "critical", "无历史单查证器") {
		t.Errorf("缺少 critical「无历史单查证器」告警，alerts=%v", fs.alerts)
	}
}

// TestIsOPENINGZombie OPENING 僵尸判定边界。
func TestIsOPENINGZombie(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		pos  pgstore.Position
		want bool
	}{
		{"OPENING 超 24h", pgstore.Position{Status: "OPENING", OpenedAt: ptrTime(now.Add(-25 * time.Hour))}, true},
		{"OPENING 刚好 24h", pgstore.Position{Status: "OPENING", OpenedAt: ptrTime(now.Add(-24 * time.Hour))}, false},
		{"OPENING 未超", pgstore.Position{Status: "OPENING", OpenedAt: ptrTime(now.Add(-23 * time.Hour))}, false},
		{"OPEN 不判", pgstore.Position{Status: "OPEN", OpenedAt: ptrTime(now.Add(-48 * time.Hour))}, false},
		{"OPENING 无 opened_at 不判", pgstore.Position{Status: "OPENING", OpenedAt: nil}, false},
	}
	for _, c := range cases {
		if got := isOPENINGZombie(&c.pos, now); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// 防止 unused 警告（strconv 在 order_history.go 用，这里确保测试包编译）
var _ = strconv.Itoa
