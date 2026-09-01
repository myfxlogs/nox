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
	cashflows   []fakeCashflow // D-159 W3a R4：兜底分支补写记录
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

// D-159 W2a R3-9：settlement 失败标记 settle_failed（fakeStore 适配 Store 接口新增方法）。
func (f *fakeStore) SetPositionNote(ctx context.Context, id int64, note string) error {
	if p, ok := f.positions[id]; ok {
		p.Note = note
	}
	return nil
}

// D-159 W3a R4：auto_close 兜底分支补写 swap/commission cashflows（fakeStore 适配 Store 接口新增方法，
// 记录调用供 A4 锚断言）。
type fakeCashflow struct {
	posID  int64
	legNo  int16
	kind   string
	venue  string
	amount float64
}

func (f *fakeStore) InsertCashflow(ctx context.Context, posID int64, legNo int16, kind, venue string, amount float64, ts time.Time) error {
	f.cashflows = append(f.cashflows, fakeCashflow{posID: posID, legNo: legNo, kind: kind, venue: venue, amount: amount})
	return nil
}

// —— fakeCloser：实现 Closer 接口 ——

type fakeCloser struct {
	calls       []int64
	failOnPos   int64      // >0 时该 posID 返回 error
	claimFailed bool       // D-159 W2a：返回 ErrClaimFailed 模拟 position 已是 CLOSING
	store       *fakeStore // D-189 R1：state-aware ResumeClose/ClaimClose（nil=旧 fallback）
}

func (c *fakeCloser) AutoClosePosition(ctx context.Context, posID int64) error {
	c.calls = append(c.calls, posID)
	if c.claimFailed {
		return pgstore.ErrClaimFailed
	}
	if c.failOnPos == posID {
		return fmt.Errorf("模拟平仓失败")
	}
	return nil
}

// D-189 R1：ClaimClose 模拟真实 store.ClaimClose（OPEN/LEGGED→CLOSING）。
// claimFailed=true → 返回 ErrClaimFailed（模拟并发已领取）。
// 设置 ClosingStartedAt=now（模拟真实 ClaimClose 的 COALESCE(now())），避免 CLOSING 扫描误判超时。
func (c *fakeCloser) ClaimClose(ctx context.Context, posID int64) error {
	if c.claimFailed {
		return pgstore.ErrClaimFailed
	}
	if c.store != nil {
		if p, ok := c.store.positions[posID]; ok {
			if p.Status != "OPEN" && p.Status != "LEGGED" {
				return pgstore.ErrClaimFailed
			}
			p.Status = "CLOSING"
			now := time.Now()
			p.ClosingStartedAt = &now
		}
	}
	return nil
}

// D-189 R1：ResumeClose 模拟真实 Pipeline.ResumeClose 状态检查。
// CLOSED/FAILED → 幂等返回 nil；CLOSING → 进入核心（AutoClosePosition）；
// 其他状态 → 返回 error（非 CLOSING）。
// claimFailed=true → 返回 ErrClaimFailed（模拟核心执行失败/并发冲突）。
// store=nil → 旧 fallback（直接调 AutoClosePosition，不检查状态）。
func (c *fakeCloser) ResumeClose(ctx context.Context, posID int64) error {
	if c.claimFailed {
		c.calls = append(c.calls, posID)
		return pgstore.ErrClaimFailed
	}
	if c.store != nil {
		if p, ok := c.store.positions[posID]; ok {
			switch p.Status {
			case "CLOSED", "FAILED":
				return nil
			case "CLOSING":
				// 进入核心
			default:
				return fmt.Errorf("position %d status=%s not CLOSING (ResumeClose only for CLOSING)", posID, p.Status)
			}
		}
	}
	return c.AutoClosePosition(ctx, posID)
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

// —— fakeSettler：实现 Settler 接口（D-087 §3.2 替换 fakeStatUpdater） ——

type fakeSettler struct {
	calls []int64
	fail  bool // D-159 W2a：返回 error 模拟 settlement 失败
}

func (s *fakeSettler) Settle(ctx context.Context, posID int64) error {
	s.calls = append(s.calls, posID)
	if s.fail {
		return fmt.Errorf("模拟 settlement 失败")
	}
	return nil
}

// —— 辅助构造 ——

func ptrFloat(v float64) *float64    { return &v }
func ptrTime(t time.Time) *time.Time { return &t }

func mkPosition(id int64, status, posType string, openedAt time.Time) *pgstore.Position {
	p := &pgstore.Position{
		ID:       id,
		Status:   status,
		Type:     posType,
		OpenedAt: ptrTime(openedAt),
	}
	// D-189 P0-4：CLOSING 状态设 ClosingStartedAt=OpenedAt（isClosingStuck 用此列判定超时）。
	if status == "CLOSING" {
		p.ClosingStartedAt = ptrTime(openedAt)
	}
	return p
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
	fsst := &fakeSettler{}
	r := &Reconciler{
		store:   fs,
		closer:  fc,
		settler: fsst,
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

	// 5. D-087 §3.2：Settle 被调（posID=100）
	if len(fsst.calls) != 1 || fsst.calls[0] != 100 {
		t.Errorf("settler 调用=%v，期望 [100]", fsst.calls)
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

	fc := &fakeCloser{store: fs} // D-189 R1：state-aware fakeCloser（ClaimClose OPEN→CLOSING + ResumeClose 状态检查）
	fsst := &fakeSettler{}
	hist := &HistoryOrder{Ticket: "Y", ClosePrice: 1.3, Profit: 5.0, Swap: 0, Commission: 0, Symbol: "GBPUSD"}
	hq := &fakeHistoryQuerier{venueKey: "52993526:mt5", results: map[string]*HistoryOrder{"Y": hist}}
	r := &Reconciler{store: fs, closer: fc, settler: fsst}
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
	// D-159 W2a S6：单腿无另一 OPEN 腿 → 走 close coordinator（AutoClosePosition），不再手动调 store.ClosePosition
	if len(fc.calls) != 1 || fc.calls[0] != 300 {
		t.Errorf("closer AutoClosePosition 调用=%v，期望 [300]", fc.calls)
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

// TestRun_NoHistoryQuerier_NoAlert 缺陷2修复：无查证器（CEX / adapter 未连）→ 不告警不计数，下轮复查。
func TestRun_NoHistoryQuerier_NoAlert(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-1 * time.Hour)
	fs.positions[800] = mkPosition(800, "OPEN", "CROSS_VENUE", opened)
	fs.legs[800] = []*pgstore.PositionLeg{
		mkOpenLeg(800, 0, "okx:spot", "V", "BUY", "BTCUSDT", 0.1), // CEX 无历史单查证器
	}

	r := &Reconciler{store: fs} // 未注入 historyQueriers

	mismatches, _ := r.Run(context.Background())
	if mismatches != 0 {
		t.Errorf("无查证器 mismatches=%d，期望 0（不计数）", mismatches)
	}
	if len(fs.alerts) != 0 {
		t.Errorf("无查证器不应告警，alerts=%v", fs.alerts)
	}
}

// TestRun_VenueQuerierTimeout_NoBlock 缺陷1修复：venue querier 阻塞 >超时，Run 返回且不 panic。
func TestRun_VenueQuerierTimeout_NoBlock(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-1 * time.Hour)
	fs.positions[900] = mkPosition(900, "OPEN", "CROSS_VENUE", opened)
	fs.legs[900] = []*pgstore.PositionLeg{
		mkOpenLeg(900, 0, "52993526:mt5", "T", "BUY", "EURUSD", 1.0),
	}

	// 阻塞 querier：QueryOpenedOrders 永不返回（模拟离线 venue）
	blockQ := &blockingQuerier{venueKey: "52993526:mt5"}
	// 阻塞查证器：QueryOrderHistory 永不返回
	blockHq := &blockingHistoryQuerier{venueKey: "52993526:mt5"}

	r := &Reconciler{store: fs, runTimeout: 100 * time.Millisecond, initialDelay: 0}
	r.SetQueriers(blockQ) // 阻塞 venue querier
	r.SetOrderHistoryQueriers(blockHq)

	done := make(chan struct{})
	go func() {
		r.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
		// Run 返回（超时生效），不 panic = pass
	case <-time.After(3 * time.Second):
		t.Fatal("Run 阻塞 >3s，超时未生效")
	}
}

// blockingQuerier 模拟离线 venue：QueryOpenedOrders 永不返回。
type blockingQuerier struct{ venueKey string }

func (q *blockingQuerier) VenueKey() string { return q.venueKey }
func (q *blockingQuerier) QueryOpenedOrders(ctx context.Context) ([]VenueOrder, error) {
	<-ctx.Done() // 等 ctx 超时
	return nil, ctx.Err()
}

// blockingHistoryQuerier 模拟离线 venue 历史查证：QueryOrderHistory 永不返回。
type blockingHistoryQuerier struct{ venueKey string }

func (q *blockingHistoryQuerier) VenueKey() string { return q.venueKey }
func (q *blockingHistoryQuerier) QueryOrderHistory(ctx context.Context, ticket string, fromDays int) (*HistoryOrder, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestIsOPENINGZombie OPENING 僵尸判定边界。
// 补充条件：无 opened_at 且无任何 legs = 僵尸（#254 执行中断无时间戳）；有腿=执行中不判。
func TestIsOPENINGZombie(t *testing.T) {
	now := time.Now()
	openingLegs := []pgstore.PositionLeg{{Phase: "OPENING"}, {Phase: "OPEN"}}
	anyLegs := []pgstore.PositionLeg{{Phase: "OPEN"}, {Phase: "CLOSE"}}
	emptyLegs := []pgstore.PositionLeg{}
	cases := []struct {
		name string
		pos  pgstore.Position
		legs []pgstore.PositionLeg
		want bool
	}{
		{"OPENING 超 24h", pgstore.Position{Status: "OPENING", OpenedAt: ptrTime(now.Add(-25 * time.Hour))}, openingLegs, true},
		{"OPENING 刚好 24h", pgstore.Position{Status: "OPENING", OpenedAt: ptrTime(now.Add(-24 * time.Hour))}, openingLegs, false},
		{"OPENING 未超", pgstore.Position{Status: "OPENING", OpenedAt: ptrTime(now.Add(-23 * time.Hour))}, openingLegs, false},
		{"OPEN 不判", pgstore.Position{Status: "OPEN", OpenedAt: ptrTime(now.Add(-48 * time.Hour))}, openingLegs, false},
		// 补充条件：无 opened_at + 无任何 legs = 僵尸（#254 执行中断无进展）
		{"OPENING 无 opened_at 空腿=僵尸", pgstore.Position{Status: "OPENING", OpenedAt: nil}, emptyLegs, true},
		// 无 opened_at 但有腿（任意 phase）= 执行中，不判僵尸（防新建仓窗口误杀）
		{"OPENING 无 opened_at 有腿不判", pgstore.Position{Status: "OPENING", OpenedAt: nil}, anyLegs, false},
		{"OPENING 无 opened_at 有 OPENING 腿不判", pgstore.Position{Status: "OPENING", OpenedAt: nil}, openingLegs, false},
	}
	for _, c := range cases {
		if got := isOPENINGZombie(&c.pos, c.legs, now); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// TestAutoClose_ClaimFailedFallback D-159 W3a R4 A4：ClaimFailed 兜底分支只用 Profit + 补写 cashflows。
// 场景：单腿 position（otherOpen=false），closer 返回 ErrClaimFailed（position 已被并发领取为 CLOSING），
// hist 有 Profit/Swap/Commission → 走兜底分支 store.ClosePosition(pnl=hist.Profit) + 补写 swap/commission cashflows。
// 断言：ClosePosition 的 realizedPnl=hist.Profit（不含 swap/commission）+ cashflows 含 swap+commission 两行。
func TestAutoClose_ClaimFailedFallback(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-2 * time.Hour)
	fs.positions[100] = mkPosition(100, "OPEN", "CROSS_VENUE", opened)
	// 单腿 → otherOpen=false → 走 ResumeClose（D-189 P0-2 删除 raw 旁路）
	fs.legs[100] = []*pgstore.PositionLeg{
		mkOpenLeg(100, 0, "52993526:mt5", "A", "BUY", "AUDJPY", 1.0),
	}

	fc := &fakeCloser{claimFailed: true} // ResumeClose 返回 ErrClaimFailed（模拟 position 已是 CLOSING）
	fsst := &fakeSettler{}
	r := &Reconciler{store: fs, closer: fc, settler: fsst}

	hist := &HistoryOrder{
		Ticket: "A", ClosePrice: 100.5,
		Profit: 10.0, Swap: 0.5, Commission: 1.0, Symbol: "AUDJPY",
	}
	goneLeg := *mkOpenLeg(100, 0, "52993526:mt5", "A", "BUY", "AUDJPY", 1.0)

	if err := r.autoClose(context.Background(), fs.positions[100], goneLeg, hist); err != nil {
		t.Fatalf("autoClose: %v", err)
	}

	// D-189 P0-2 断言①：ResumeClose 被调用（fakeCloser.calls 记录调用）
	if len(fc.calls) == 0 {
		t.Fatalf("ResumeClose 未被调用，期望至少 1 次")
	}

	// D-189 P0-2 断言②：store.ClosePosition 不被直接调用（raw 旁路已删除）
	if fs.closeCalls != 0 {
		t.Errorf("store.ClosePosition 直接调用=%d, want 0 (D-189 P0-2 删除 raw 旁路)", fs.closeCalls)
	}

	// D-189 P0-2 断言③：raw cashflow 不被直接写入（由 ResumeClose 内部 writeCloseCashflows 处理）
	// fakeCloser.ResumeClose 返回 ErrClaimFailed → 不实际写 cashflow，但 auto_close 也不应自行写。
	for i := range fs.cashflows {
		c := &fs.cashflows[i]
		if c.kind == "swap" || c.kind == "commission" {
			t.Errorf("auto_close 不应直接写 raw cashflow (kind=%s amount=%.4f)，D-189 P0-2 删除 raw 旁路", c.kind, c.amount)
		}
	}
}

// TestFakeCloser_ResumeClose_StateCheck D-189 R1 T1：fakeCloser.ResumeClose 模拟真实 Pipeline.ResumeClose 状态检查。
// 非 CLOSING 状态 → 返回 error（修复前 fakeCloser 直接调 AutoClosePosition 不检查状态，测试全绿但生产 break）。
func TestFakeCloser_ResumeClose_StateCheck(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-1 * time.Hour)

	// 场景①：OPEN → ResumeClose 返回 error（非 CLOSING）
	fs.positions[100] = mkPosition(100, "OPEN", "CROSS_VENUE", opened)
	fc := &fakeCloser{store: fs}
	err := fc.ResumeClose(context.Background(), 100)
	if err == nil {
		t.Error("fakeCloser.ResumeClose on OPEN should return error (not CLOSING)")
	}

	// 场景②：CLOSING → ResumeClose 进入核心（调 AutoClosePosition）
	fs.positions[101] = mkPosition(101, "CLOSING", "CROSS_VENUE", opened)
	fc2 := &fakeCloser{store: fs}
	err = fc2.ResumeClose(context.Background(), 101)
	if err != nil {
		t.Errorf("fakeCloser.ResumeClose on CLOSING should enter core (AutoClosePosition), got: %v", err)
	}
	if len(fc2.calls) != 1 || fc2.calls[0] != 101 {
		t.Errorf("CLOSING → AutoClosePosition 调用=%v，期望 [101]", fc2.calls)
	}

	// 场景③：CLOSED → ResumeClose 幂等返回 nil（不调 AutoClosePosition）
	fs.positions[102] = mkPosition(102, "CLOSED", "CROSS_VENUE", opened)
	fc3 := &fakeCloser{store: fs}
	err = fc3.ResumeClose(context.Background(), 102)
	if err != nil {
		t.Errorf("fakeCloser.ResumeClose on CLOSED should return nil (idempotent), got: %v", err)
	}
	if len(fc3.calls) != 0 {
		t.Errorf("CLOSED → 不应调 AutoClosePosition，calls=%v", fc3.calls)
	}

	// 场景④：ClaimClose OPEN→CLOSING → ResumeClose 成功（端到端状态流转）
	fs.positions[103] = mkPosition(103, "OPEN", "CROSS_VENUE", opened)
	fc4 := &fakeCloser{store: fs}
	if err := fc4.ClaimClose(context.Background(), 103); err != nil {
		t.Fatalf("ClaimClose on OPEN: %v", err)
	}
	if fs.positions[103].Status != "CLOSING" {
		t.Errorf("ClaimClose 后 status=%s，期望 CLOSING", fs.positions[103].Status)
	}
	if err := fc4.ResumeClose(context.Background(), 103); err != nil {
		t.Errorf("ResumeClose after ClaimClose: %v", err)
	}
}

// 防止 unused 警告（strconv 在 order_history.go 用，这里确保测试包编译）
var _ = strconv.Itoa
