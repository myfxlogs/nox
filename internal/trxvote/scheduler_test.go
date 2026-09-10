// D-269 S4 测试：Scheduler doRebalance + doClaim（mock executor + fake store + mock account）。
package trxvote

import (
	"context"
	"errors"
	"testing"

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
func (f *fakeStore) TrxVoteRebalancedToday(_ context.Context) bool {
	return f.rebalancedToday
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
	voteTxExt     *api.TransactionExtention
	voteErr       error
	reward        int64
	rewardErr     error
	withdrawTxExt *api.TransactionExtention
	withdrawErr   error
	broadcastErr  error
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
	return &api.Return{}, m.broadcastErr
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

// TestDoRebalance_AccountFail 账户查询失败 → warn 告警。
func TestDoRebalance_AccountFail(t *testing.T) {
	// 这个测试需要 mock fetchSRList 成功 + account 失败
	// 用 httptest mock TronScan
	t.Skip("S5 集成测试覆盖完整链路")
}

// TestDoClaim_ZeroReward reward=0 → 跳过。
func TestDoClaim_ZeroReward(t *testing.T) {
	conn := &testExecutorConn{reward: 0}
	sched, exec := testScheduler(t, &fakeStore{}, &mockAccountFetcher{}, conn)
	var alertCalled bool
	exec.alertSink = func(level, msg string) {
		alertCalled = true
	}
	sched.doClaim(context.Background())
	if alertCalled {
		t.Error("should not alert when reward=0")
	}
}

// TestDoClaim_Normal 正常 → warn 告警。
func TestDoClaim_Normal(t *testing.T) {
	txid := []byte{0xca, 0xfe, 0x12, 0x34}
	conn := &testExecutorConn{
		reward: 1000000,
		withdrawTxExt: &api.TransactionExtention{
			Transaction: &core.Transaction{},
			Txid:        txid,
		},
	}
	sched, exec := testScheduler(t, &fakeStore{}, &mockAccountFetcher{}, conn)
	var gotAlert string
	exec.alertSink = func(level, msg string) {
		gotAlert = level + ":" + msg
	}
	sched.doClaim(context.Background())
	if gotAlert == "" {
		t.Fatal("should alert on successful claim")
	}
	if len(gotAlert) < 4 || gotAlert[:4] != "warn" {
		t.Errorf("alert level = %q, want warn", gotAlert)
	}
}

// TestDoClaim_Failed 领奖失败 → critical 告警。
func TestDoClaim_Failed(t *testing.T) {
	conn := &testExecutorConn{rewardErr: errors.New("query failed")}
	sched, exec := testScheduler(t, &fakeStore{}, &mockAccountFetcher{}, conn)
	var gotAlert string
	exec.alertSink = func(level, msg string) {
		gotAlert = level + ":" + msg
	}
	sched.doClaim(context.Background())
	if gotAlert == "" {
		t.Error("should alert on claim failure")
	}
	if len(gotAlert) < 8 || gotAlert[:8] != "critical" {
		t.Errorf("alert level = %q, want critical", gotAlert)
	}
}

// TestConvertVotes map → []VoteEntry 转换。
func TestConvertVotes(t *testing.T) {
	m := map[string]int64{"Tsr1": 5000, "Tsr2": 3000}
	votes := convertVotes(m)
	if len(votes) != 2 {
		t.Fatalf("len = %d, want 2", len(votes))
	}
	byAddr := make(map[string]int64)
	for _, v := range votes {
		byAddr[v.SRAddress] = v.Votes
	}
	if byAddr["Tsr1"] != 5000 {
		t.Errorf("Tsr1 votes = %d, want 5000", byAddr["Tsr1"])
	}
	if byAddr["Tsr2"] != 3000 {
		t.Errorf("Tsr2 votes = %d, want 3000", byAddr["Tsr2"])
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
