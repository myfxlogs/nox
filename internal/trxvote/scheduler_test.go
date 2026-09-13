// D-269 S4 测试：Scheduler doRebalance + ClaimCheck（mock executor + fake store + mock account）。
// D-293 S2：doClaim 测试适配为 ClaimCheck 路径（三态+冷却+hook panic）。
package trxvote

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/fbsobreira/gotron-sdk/pkg/account"
	"github.com/fbsobreira/gotron-sdk/pkg/proto/api"
	"github.com/fbsobreira/gotron-sdk/pkg/proto/core"
)

// fakeStore 实现 Store 接口用于测试。
type fakeStore struct {
	insertedRebalance *TRXVoteRebalanceRecord
	insertedID        int64
	insertErr         error
	updateID          int64
	updateTxHash      string
	updateStatus      string
	updateErr         error
	rebalancedToday   bool
	// D-293 S3：防抖查询 mock
	lastExecutedAt  time.Time
	lastExecutedErr error
}

func (f *fakeStore) InsertTrxVoteRebalance(_ context.Context, rec TRXVoteRebalanceRecord) (int64, error) {
	f.insertedRebalance = &rec
	f.insertedID++
	return f.insertedID, f.insertErr
}
func (f *fakeStore) UpdateTrxVoteRebalanceResult(_ context.Context, id int64, txHash, status string) error {
	f.updateID = id
	f.updateTxHash = txHash
	f.updateStatus = status
	return f.updateErr
}
func (f *fakeStore) TrxVoteRebalancedToday(_ context.Context, _ int) bool {
	return f.rebalancedToday
}
func (f *fakeStore) LastExecutedRebalanceAt(_ context.Context) (time.Time, error) {
	return f.lastExecutedAt, f.lastExecutedErr
}

// mockAccountFetcher 实现 AccountFetcher 接口。
type mockAccountFetcher struct {
	acc *account.Account
	err error
}

func (m *mockAccountFetcher) GetAccountDetailedCtx(_ context.Context, _ string) (*account.Account, error) {
	return m.acc, m.err
}

// mockExecutorConn 实现 TronClient（用于构造 Executor）。
type testExecutorConn struct {
	voteTxExt       *api.TransactionExtention
	voteErr         error
	reward          int64
	rewardErr       error
	withdrawTxExt   *api.TransactionExtention
	withdrawErr     error
	broadcastErr    error
	broadcastCalled bool // D-293 S2：断言 ClaimCheck 零 RPC 路径未广播
	// 带宽预检 mock（nil=返回充足带宽）
	acctResource *api.AccountResourceMessage
	acctResErr   error
}

func (m *testExecutorConn) VoteWitnessAccountCtx(_ context.Context, _ string, _ map[string]int64) (*api.TransactionExtention, error) {
	return m.voteTxExt, m.voteErr
}
func (m *testExecutorConn) WithdrawBalanceCtx(_ context.Context, _ string) (*api.TransactionExtention, error) {
	return m.withdrawTxExt, m.withdrawErr
}
func (m *testExecutorConn) GetRewardsInfoCtx(_ context.Context, _ string) (int64, error) {
	return m.reward, m.rewardErr
}
func (m *testExecutorConn) BroadcastCtx(_ context.Context, _ *core.Transaction) (*api.Return, error) {
	m.broadcastCalled = true
	return &api.Return{}, m.broadcastErr
}
func (m *testExecutorConn) GetAccountResourceCtx(_ context.Context, _ string) (*api.AccountResourceMessage, error) {
	if m.acctResErr != nil {
		return nil, m.acctResErr
	}
	if m.acctResource != nil {
		return m.acctResource, nil
	}
	// 默认返回充足带宽（free 600 used 0 + staked 1285 used 0 = 1885 BP）
	return &api.AccountResourceMessage{
		FreeNetLimit: 600,
		FreeNetUsed:  0,
		NetLimit:     1285,
		NetUsed:      0,
	}, nil
}

// testScheduler 构造带 mock 的 Scheduler。
func testScheduler(t *testing.T, store Store, accFetcher AccountFetcher, conn TronClient) (*Scheduler, *Executor) {
	t.Helper()
	// 32 字节确定性私钥（仅测试用，非全零避免 SignTransaction 拒绝）
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 1)
	}
	privKey, _ := btcec.PrivKeyFromBytes(keyBytes)
	var alerts []string
	alertSink := func(level, msg string) {
		alerts = append(alerts, level+":"+msg)
	}
	_ = alerts // 测试可检查 alerts
	executor := NewExecutor(conn, privKey, "Tfrom", alertSink)
	sched := NewScheduler(executor, store, "Tfrom", "http://test", DefaultConfig(), accFetcher)
	return sched, executor
}

// TestDoRebalance_AlreadyDone 今日已改投 → 跳过。
func TestDoRebalance_AlreadyDone(t *testing.T) {
	store := &fakeStore{rebalancedToday: true}
	sched, _ := testScheduler(t, store, &mockAccountFetcher{}, &testExecutorConn{})
	sched.doRebalance(context.Background())
	if store.insertedRebalance != nil {
		t.Error("should not insert when already rebalanced today")
	}
}

// TestDoRebalance_NoNeedRebalance NeedRebalance=false → 只落库不执行。
func TestDoRebalance_NoNeedRebalance(t *testing.T) {
	// 这个测试需要完整 mock（httptest + fake store + mock account），S5 集成测试覆盖
	t.Skip("S5 集成测试覆盖完整链路")
}

// TestDoRebalance_FetchFail fetch SR 失败 → warn 告警。
func TestDoRebalance_FetchFail(t *testing.T) {
	store := &fakeStore{rebalancedToday: false}
	// tronscanBase 指向无效地址 → fetch 失败（HTTP 连接失败）
	sched, exec := testScheduler(t, store, &mockAccountFetcher{}, &testExecutorConn{})
	sched.tronscanBase = "http://invalid-test-host-99999"
	var gotAlert string
	exec.alertSink = func(level, msg string) {
		gotAlert = level + ":" + msg
	}
	sched.doRebalance(context.Background())
	if gotAlert == "" {
		t.Error("should alert on fetch failure")
	}
	if store.insertedRebalance != nil {
		t.Error("should not insert on fetch failure")
	}
}

// TestDoRebalance_AccountFail 账户查询失败 → warn 告警 + 不落库。
func TestDoRebalance_AccountFail(t *testing.T) {
	// httptest mock TronScan 成功 + account 查询失败 → warn 告警
	srFixture := `[{"address":"SR1","name":"SR1","annualizedRate":"5.0","brokerage":0,"realTimeVotes":100000,"votes":100000,"producer":true,"producedTotal":100}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(srFixture))
	}))
	defer srv.Close()
	SetHTTPClient(srv.Client())
	defer SetHTTPClient(&http.Client{})

	store := &fakeStore{rebalancedToday: false}
	accFetcher := &mockAccountFetcher{err: errors.New("account query failed")}
	sched, exec := testScheduler(t, store, accFetcher, &testExecutorConn{})
	sched.tronscanBase = srv.URL
	var gotAlert string
	exec.alertSink = func(level, msg string) {
		gotAlert = level + ":" + msg
	}

	sched.doRebalance(context.Background())

	// account 查询失败 → warn 告警（scheduler.go:138 发 warn）
	if gotAlert == "" {
		t.Fatal("should alert on account query failure")
	}
	if len(gotAlert) < 4 || gotAlert[:4] != "warn" {
		t.Errorf("alert level = %q, want warn", gotAlert)
	}
	// 不落库（account 失败在落库之前 return）
	if store.insertedRebalance != nil {
		t.Error("should not insert on account query failure")
	}
}

// TestDoRebalance_MinIntervalSkip D-293 S3：距上次 executed < MinInterval → 跳过（不落库）。
func TestDoRebalance_MinIntervalSkip(t *testing.T) {
	store := &fakeStore{
		rebalancedToday: false,
		lastExecutedAt:  time.Now().Add(-30 * time.Minute), // 30min 前 executed
	}
	sched, _ := testScheduler(t, store, &mockAccountFetcher{}, &testExecutorConn{})
	sched.config.MinInterval = 2 * time.Hour // MinInterval=2h
	sched.doRebalance(context.Background())
	if store.insertedRebalance != nil {
		t.Error("should not insert when last executed < MinInterval (debounce)")
	}
}

// TestDoRebalance_MinIntervalExpired D-293 S3：距上次 executed ≥ MinInterval → 放行（落库）。
func TestDoRebalance_MinIntervalExpired(t *testing.T) {
	// 需要 httptest mock SR list 成功才能走到落库
	srFixture := `[{"address":"SR1","name":"SR1","annualizedRate":"5.0","brokerage":0,"realTimeVotes":100000,"votes":100000,"producer":true,"producedTotal":100}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(srFixture))
	}))
	defer srv.Close()
	SetHTTPClient(srv.Client())
	defer SetHTTPClient(&http.Client{})

	store := &fakeStore{
		rebalancedToday: false,
		lastExecutedAt:  time.Now().Add(-3 * time.Hour), // 3h 前 executed > 2h MinInterval
	}
	accFetcher := &mockAccountFetcher{acc: &account.Account{
		Votes:     map[string]int64{"SR1": 18070},
		TronPower: 18070,
	}}
	sched, _ := testScheduler(t, store, accFetcher, &testExecutorConn{})
	sched.tronscanBase = srv.URL
	sched.config.MinInterval = 2 * time.Hour
	sched.doRebalance(context.Background())
	if store.insertedRebalance == nil {
		t.Fatal("should insert when last executed ≥ MinInterval (debounce expired)")
	}
}

// TestDoRebalance_MinIntervalZero D-293 S3：MinInterval=0 → 不防抖（每次评估都可执行）。
func TestDoRebalance_MinIntervalZero(t *testing.T) {
	srFixture := `[{"address":"SR1","name":"SR1","annualizedRate":"5.0","brokerage":0,"realTimeVotes":100000,"votes":100000,"producer":true,"producedTotal":100}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(srFixture))
	}))
	defer srv.Close()
	SetHTTPClient(srv.Client())
	defer SetHTTPClient(&http.Client{})

	store := &fakeStore{
		rebalancedToday: false,
		lastExecutedAt:  time.Now().Add(-1 * time.Minute), // 1min 前 executed
	}
	accFetcher := &mockAccountFetcher{acc: &account.Account{
		Votes:     map[string]int64{"SR1": 18070},
		TronPower: 18070,
	}}
	sched, _ := testScheduler(t, store, accFetcher, &testExecutorConn{})
	sched.tronscanBase = srv.URL
	sched.config.MinInterval = 0 // MinInterval=0 → 不防抖
	sched.doRebalance(context.Background())
	if store.insertedRebalance == nil {
		t.Fatal("should insert when MinInterval=0 (no debounce)")
	}
}

// TestClaimCheck_BelowThreshold rewardSun ≤10e6 → 零 RPC 跳过（无告警）。
func TestClaimCheck_BelowThreshold(t *testing.T) {
	conn := &testExecutorConn{reward: 0}
	sched, exec := testScheduler(t, &fakeStore{}, &mockAccountFetcher{}, conn)
	var alertCalled bool
	exec.alertSink = func(level, msg string) {
		alertCalled = true
	}
	sched.ClaimCheck(context.Background(), 5_000_000) // 5 TRX < 10 TRX 阈值
	if alertCalled {
		t.Error("should not alert when reward ≤10e6 (zero RPC skip)")
	}
	if conn.broadcastCalled {
		t.Error("should not call broadcast when reward ≤10e6")
	}
}

// TestClaimCheck_BandwidthLow 带宽不足 → 跳过领奖 + warn 告警。
func TestClaimCheck_BandwidthLow(t *testing.T) {
	conn := &testExecutorConn{
		reward: 20_000_000, // 20 TRX > 阈值，但带宽不足
		acctResource: &api.AccountResourceMessage{
			FreeNetLimit: 600,
			FreeNetUsed:  590, // 剩余 10 BP
			NetLimit:     1285,
			NetUsed:      1280, // 剩余 5 BP → 总 15 BP < 250
		},
	}
	sched, exec := testScheduler(t, &fakeStore{}, &mockAccountFetcher{}, conn)
	var gotAlert string
	exec.alertSink = func(level, msg string) {
		gotAlert = level + ":" + msg
	}
	sched.ClaimCheck(context.Background(), 20_000_000)
	if gotAlert == "" {
		t.Fatal("should alert when bandwidth insufficient")
	}
	if !strings.Contains(gotAlert, "带宽不足") {
		t.Errorf("alert should mention 带宽不足: %q", gotAlert)
	}
}

// TestClaimCheck_Normal 正常 → warn 告警（reward > 10 TRX 阈值 + 带宽充足）。
func TestClaimCheck_Normal(t *testing.T) {
	txid := []byte{0xca, 0xfe, 0x12, 0x34}
	conn := &testExecutorConn{
		reward:        15_000_000, // 15 TRX > 10 TRX 阈值
		withdrawTxExt: makeTxExt(txid),
	}
	sched, exec := testScheduler(t, &fakeStore{}, &mockAccountFetcher{}, conn)
	var gotAlert string
	exec.alertSink = func(level, msg string) {
		gotAlert = level + ":" + msg
	}
	sched.ClaimCheck(context.Background(), 15_000_000)
	if gotAlert == "" {
		t.Fatal("should alert on successful claim")
	}
	if len(gotAlert) < 4 || gotAlert[:4] != "warn" {
		t.Errorf("alert level = %q, want warn", gotAlert)
	}
}

// TestClaimCheck_Failed 领奖失败 → critical 告警 + 设置失败冷却时间戳。
func TestClaimCheck_Failed(t *testing.T) {
	conn := &testExecutorConn{rewardErr: errors.New("query failed")}
	sched, exec := testScheduler(t, &fakeStore{}, &mockAccountFetcher{}, conn)
	var gotAlert string
	exec.alertSink = func(level, msg string) {
		gotAlert = level + ":" + msg
	}
	sched.ClaimCheck(context.Background(), 15_000_000) // >10e6 触发 ClaimReward
	if gotAlert == "" {
		t.Error("should alert on claim failure")
	}
	if len(gotAlert) < 8 || gotAlert[:8] != "critical" {
		t.Errorf("alert level = %q, want critical", gotAlert)
	}
	if sched.lastClaimFailAt.IsZero() {
		t.Error("lastClaimFailAt should be set on failure")
	}
}

// TestClaimCheck_Cooldown 失败冷却 <15min → 跳过（无 RPC 无告警）。
func TestClaimCheck_Cooldown(t *testing.T) {
	conn := &testExecutorConn{reward: 15_000_000}
	sched, exec := testScheduler(t, &fakeStore{}, &mockAccountFetcher{}, conn)
	// 模拟 5 分钟前失败
	sched.lastClaimFailAt = time.Now().Add(-5 * time.Minute)
	var alertCalled bool
	exec.alertSink = func(level, msg string) {
		alertCalled = true
	}
	sched.ClaimCheck(context.Background(), 15_000_000) // >10e6 但在冷却期
	if alertCalled {
		t.Error("should not alert when cooling down (<15min since last fail)")
	}
	if conn.broadcastCalled {
		t.Error("should not call broadcast when cooling down")
	}
}

// TestClaimCheck_CooldownExpired 失败冷却 ≥15min → 放行执行。
func TestClaimCheck_CooldownExpired(t *testing.T) {
	txid := []byte{0xca, 0xfe, 0x12, 0x34}
	conn := &testExecutorConn{
		reward:        15_000_000,
		withdrawTxExt: makeTxExt(txid),
	}
	sched, exec := testScheduler(t, &fakeStore{}, &mockAccountFetcher{}, conn)
	// 模拟 16 分钟前失败（超过 15min 冷却）
	sched.lastClaimFailAt = time.Now().Add(-16 * time.Minute)
	var gotAlert string
	exec.alertSink = func(level, msg string) {
		gotAlert = level + ":" + msg
	}
	sched.ClaimCheck(context.Background(), 15_000_000)
	if gotAlert == "" {
		t.Fatal("should proceed when cooldown expired (≥15min)")
	}
}

// TestConvertVotes map → []VoteEntry 转换（TRX 单位，1 TRX = 1 vote）。
func TestConvertVotes(t *testing.T) {
	m := map[string]int64{"Tsr1": 5000, "Tsr2": 3000} // TRX
	votes := convertVotes(m)
	if len(votes) != 2 {
		t.Fatalf("len = %d, want 2", len(votes))
	}
	byAddr := make(map[string]float64)
	for _, v := range votes {
		byAddr[v.SRAddress] = v.Votes
	}
	if byAddr["Tsr1"] != 5000 {
		t.Errorf("Tsr1 votes = %f, want 5000 TRX", byAddr["Tsr1"])
	}
	if byAddr["Tsr2"] != 3000 {
		t.Errorf("Tsr2 votes = %f, want 3000 TRX", byAddr["Tsr2"])
	}
}

// TestFormatRebalanceAlert 告警格式。
func TestFormatRebalanceAlert(t *testing.T) {
	d := RebalanceDecision{
		CurrentYield: 100.0,
		TargetYield:  120.0,
		Improvement:  20.0,
	}
	alert := formatRebalanceAlert("deadbeef1234567890abcdef", d)
	if alert == "" {
		t.Error("alert should not be empty")
	}
	// txHash 应截断到 16 位
	if len(alert) < 16 {
		t.Error("alert too short")
	}
}

// TestFormatClaimAlert 告警格式。
func TestFormatClaimAlert(t *testing.T) {
	alert := formatClaimAlert("deadbeef1234567890abcdef", 1000000)
	if alert == "" {
		t.Error("alert should not be empty")
	}
}
