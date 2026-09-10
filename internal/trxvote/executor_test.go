// D-269 S3 测试：Executor Vote + ClaimReward（mock TronClient）。
package trxvote

import (
	"context"
	"errors"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/fbsobreira/gotron-sdk/pkg/proto/api"
	"github.com/fbsobreira/gotron-sdk/pkg/proto/core"
)

// mockTronClient 实现 TronClient 接口用于测试。
type mockTronClient struct {
	voteTxExt       *api.TransactionExtention
	voteErr         error
	withdrawTxExt   *api.TransactionExtention
	withdrawErr     error
	reward          int64
	rewardErr       error
	broadcastErr    error
	broadcastCalled bool
}

func (m *mockTronClient) VoteWitnessAccountCtx(_ context.Context, _ string, _ map[string]int64) (*api.TransactionExtention, error) {
	return m.voteTxExt, m.voteErr
}
func (m *mockTronClient) WithdrawBalanceCtx(_ context.Context, _ string) (*api.TransactionExtention, error) {
	return m.withdrawTxExt, m.withdrawErr
}
func (m *mockTronClient) GetRewardsInfoCtx(_ context.Context, _ string) (int64, error) {
	return m.reward, m.rewardErr
}
func (m *mockTronClient) BroadcastCtx(_ context.Context, _ *core.Transaction) (*api.Return, error) {
	m.broadcastCalled = true
	return &api.Return{}, m.broadcastErr
}

// testPrivKey 生成测试用私钥（确定性，never used for real TRX）。
func testPrivKey(t *testing.T) *btcec.PrivateKey {
	t.Helper()
	// 32 字节确定性私钥（仅测试用）
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 1)
	}
	privKey, _ := btcec.PrivKeyFromBytes(keyBytes)
	return privKey
}

// makeTxExt 构造带 txid 的 TransactionExtention。
// txidBytes 是原始字节，hex.EncodeToString(txidBytes) 是期望的 txHash。
func makeTxExt(txidBytes []byte) *api.TransactionExtention {
	return &api.TransactionExtention{
		Transaction: &core.Transaction{},
		Txid:        txidBytes,
	}
}

// TestVote_Normal 正常流程 → 返回 txHash。
func TestVote_Normal(t *testing.T) {
	txid := []byte{0xde, 0xad, 0xbe, 0xef, 0x12, 0x34, 0x56, 0x78}
	mc := &mockTronClient{
		voteTxExt: makeTxExt(txid),
	}
	e := NewExecutor(mc, testPrivKey(t), "Tfrom", nil)

	txHash, err := e.Vote(context.Background(), []VoteEntry{{SRAddress: "Tsr1", Votes: 5000}})
	if err != nil {
		t.Fatalf("Vote: %v", err)
	}
	if txHash != "deadbeef12345678" {
		t.Errorf("txHash = %q, want deadbeef12345678", txHash)
	}
	if !mc.broadcastCalled {
		t.Error("Broadcast not called")
	}
}

// TestVote_BuildFailed 构建失败 → error。
func TestVote_BuildFailed(t *testing.T) {
	mc := &mockTronClient{
		voteErr: errors.New("build error"),
	}
	e := NewExecutor(mc, testPrivKey(t), "Tfrom", nil)

	_, err := e.Vote(context.Background(), []VoteEntry{{SRAddress: "Tsr1", Votes: 5000}})
	if err == nil {
		t.Fatal("Vote should return error on build failure")
	}
	if mc.broadcastCalled {
		t.Error("Broadcast should not be called on build failure")
	}
}

// TestVote_NilTransaction nil transaction → error。
func TestVote_NilTransaction(t *testing.T) {
	mc := &mockTronClient{
		voteTxExt: &api.TransactionExtention{Transaction: nil},
	}
	e := NewExecutor(mc, testPrivKey(t), "Tfrom", nil)

	_, err := e.Vote(context.Background(), []VoteEntry{{SRAddress: "Tsr1", Votes: 5000}})
	if err == nil {
		t.Fatal("Vote should return error on nil transaction")
	}
}

// TestVote_BroadcastFailed 广播失败 → error。
func TestVote_BroadcastFailed(t *testing.T) {
	mc := &mockTronClient{
		voteTxExt:    makeTxExt([]byte{0xde, 0xad}),
		broadcastErr: errors.New("broadcast error"),
	}
	e := NewExecutor(mc, testPrivKey(t), "Tfrom", nil)

	_, err := e.Vote(context.Background(), []VoteEntry{{SRAddress: "Tsr1", Votes: 5000}})
	if err == nil {
		t.Fatal("Vote should return error on broadcast failure")
	}
}

// TestClaimReward_ZeroReward reward=0 → 跳过（返回空 txHash, 0, nil）。
func TestClaimReward_ZeroReward(t *testing.T) {
	mc := &mockTronClient{
		reward: 0,
	}
	e := NewExecutor(mc, testPrivKey(t), "Tfrom", nil)

	txHash, amount, err := e.ClaimReward(context.Background())
	if err != nil {
		t.Fatalf("ClaimReward: %v", err)
	}
	if txHash != "" {
		t.Errorf("txHash = %q, want empty (reward=0 skip)", txHash)
	}
	if amount != 0 {
		t.Errorf("amount = %d, want 0", amount)
	}
	if mc.broadcastCalled {
		t.Error("Broadcast should not be called when reward=0")
	}
}

// TestClaimReward_Normal 正常流程 → 返回 txHash + amount。
func TestClaimReward_Normal(t *testing.T) {
	txid := []byte{0xca, 0xfe, 0x12, 0x34}
	mc := &mockTronClient{
		reward:        1000000, // 1M sun = 1 TRX
		withdrawTxExt: makeTxExt(txid),
	}
	e := NewExecutor(mc, testPrivKey(t), "Tfrom", nil)

	txHash, amount, err := e.ClaimReward(context.Background())
	if err != nil {
		t.Fatalf("ClaimReward: %v", err)
	}
	if txHash != "cafe1234" {
		t.Errorf("txHash = %q, want cafe1234", txHash)
	}
	if amount != 1000000 {
		t.Errorf("amount = %d, want 1000000", amount)
	}
	if !mc.broadcastCalled {
		t.Error("Broadcast not called")
	}
}

// TestClaimReward_QueryFailed 查询失败 → error。
func TestClaimReward_QueryFailed(t *testing.T) {
	mc := &mockTronClient{
		rewardErr: errors.New("query error"),
	}
	e := NewExecutor(mc, testPrivKey(t), "Tfrom", nil)

	_, _, err := e.ClaimReward(context.Background())
	if err == nil {
		t.Fatal("ClaimReward should return error on query failure")
	}
}

// TestClaimReward_BuildFailed 构建失败 → error。
func TestClaimReward_BuildFailed(t *testing.T) {
	mc := &mockTronClient{
		reward:      1000000,
		withdrawErr: errors.New("build error"),
	}
	e := NewExecutor(mc, testPrivKey(t), "Tfrom", nil)

	_, _, err := e.ClaimReward(context.Background())
	if err == nil {
		t.Fatal("ClaimReward should return error on build failure")
	}
}

// TestClaimReward_BroadcastFailed 广播失败 → error。
func TestClaimReward_BroadcastFailed(t *testing.T) {
	mc := &mockTronClient{
		reward:        1000000,
		withdrawTxExt: makeTxExt([]byte{0xca, 0xfe}),
		broadcastErr:  errors.New("broadcast error"),
	}
	e := NewExecutor(mc, testPrivKey(t), "Tfrom", nil)

	_, _, err := e.ClaimReward(context.Background())
	if err == nil {
		t.Fatal("ClaimReward should return error on broadcast failure")
	}
}

// TestAlert_NilSink alertSink=nil 不 panic。
func TestAlert_NilSink(t *testing.T) {
	e := NewExecutor(&mockTronClient{}, testPrivKey(t), "Tfrom", nil)
	e.Alert("warn", "test message") // should not panic
}

// TestAlert_WithSink alertSink 非 nil 正常调用。
func TestAlert_WithSink(t *testing.T) {
	called := false
	sink := func(level, msg string) {
		called = true
		if level != "warn" {
			t.Errorf("level = %q, want warn", level)
		}
		if msg != "test" {
			t.Errorf("msg = %q, want test", msg)
		}
	}
	e := NewExecutor(&mockTronClient{}, testPrivKey(t), "Tfrom", sink)
	e.Alert("warn", "test")
	if !called {
		t.Error("alertSink not called")
	}
}
