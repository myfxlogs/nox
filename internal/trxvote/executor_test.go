// D-269 S3 测试：Executor Vote + ClaimReward（mock TronClient）。
// D-293 S1：tx hash 重算红绿锚（变异后重算 ≠ 原 txid，证明修复必要+正确）。
package trxvote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/fbsobreira/gotron-sdk/pkg/proto/api"
	"github.com/fbsobreira/gotron-sdk/pkg/proto/core"
	"google.golang.org/protobuf/proto"
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
	// 捕获广播时交易的 PermissionId（验证多签权限设置）。
	broadcastPermID int32
	// 带宽查询 mock（nil=充足带宽）
	acctResource *api.AccountResourceMessage
	acctResErr   error
	// D-137：自发 tx 回执查询 mock
	txInfo    *core.TransactionInfo
	txInfoErr error
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
func (m *mockTronClient) BroadcastCtx(_ context.Context, tx *core.Transaction) (*api.Return, error) {
	m.broadcastCalled = true
	if tx != nil && tx.RawData != nil && len(tx.RawData.Contract) > 0 {
		m.broadcastPermID = tx.RawData.Contract[0].GetPermissionId()
	}
	return &api.Return{}, m.broadcastErr
}
func (m *mockTronClient) GetTransactionInfoByIDCtx(_ context.Context, _ string) (*core.TransactionInfo, error) {
	return m.txInfo, m.txInfoErr
}
func (m *mockTronClient) GetAccountResourceCtx(_ context.Context, _ string) (*api.AccountResourceMessage, error) {
	if m.acctResErr != nil {
		return nil, m.acctResErr
	}
	if m.acctResource != nil {
		return m.acctResource, nil
	}
	return &api.AccountResourceMessage{FreeNetLimit: 600, NetLimit: 1285}, nil
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

// makeTxExt 构造带 txid + RawData.Contract 的 TransactionExtention。
// txidBytes 是原始字节，hex.EncodeToString(txidBytes) 是期望的 txHash。
// Contract 必须存在，executor 在签名前设 PermissionId 到 Contract[0]。
func makeTxExt(txidBytes []byte) *api.TransactionExtention {
	return &api.TransactionExtention{
		Transaction: &core.Transaction{
			RawData: &core.TransactionRaw{
				Contract: []*core.Transaction_Contract{{}},
			},
		},
		Txid: txidBytes,
	}
}

// TestVote_Normal 正常流程 → 返回重算 txHash（D-293 F1：变异后重算，非 txExt.GetTxid()）。
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
	// D-293 F1：返回的是重算 hash（变异后），非 txExt.GetTxid()（变异前）
	want := recomputeHash(t, mc.voteTxExt.GetTransaction())
	if txHash != want {
		t.Errorf("txHash = %q, want recompute %q", txHash, want)
	}
	if txHash == "deadbeef12345678" {
		t.Error("txHash == txExt.GetTxid()，变异后未重算——F1 bug 仍在")
	}
	if !mc.broadcastCalled {
		t.Error("Broadcast not called")
	}
	if mc.broadcastPermID != ArbPermissionId {
		t.Errorf("PermissionId = %d, want %d (arb)", mc.broadcastPermID, ArbPermissionId)
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

// TestClaimReward_Normal 正常流程 → 返回重算 txHash + amount（reward ≥ 10 TRX 阈值）。
func TestClaimReward_Normal(t *testing.T) {
	txid := []byte{0xca, 0xfe, 0x12, 0x34}
	mc := &mockTronClient{
		reward:        15_000_000, // 15 TRX > 10 TRX 阈值
		withdrawTxExt: makeTxExt(txid),
	}
	e := NewExecutor(mc, testPrivKey(t), "Tfrom", nil)

	txHash, amount, err := e.ClaimReward(context.Background())
	if err != nil {
		t.Fatalf("ClaimReward: %v", err)
	}
	// D-293 F1：返回的是重算 hash（变异后），非 txExt.GetTxid()（变异前）
	want := recomputeHash(t, mc.withdrawTxExt.GetTransaction())
	if txHash != want {
		t.Errorf("txHash = %q, want recompute %q", txHash, want)
	}
	if txHash == "cafe1234" {
		t.Error("txHash == txExt.GetTxid()，变异后未重算——F1 bug 仍在")
	}
	if amount != 15_000_000 {
		t.Errorf("amount = %d, want 15000000", amount)
	}
	if !mc.broadcastCalled {
		t.Error("Broadcast not called")
	}
	if mc.broadcastPermID != ArbPermissionId {
		t.Errorf("PermissionId = %d, want %d (arb)", mc.broadcastPermID, ArbPermissionId)
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
		reward:      15_000_000, // ≥ 10 TRX 阈值
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
		reward:        15_000_000, // ≥ 10 TRX 阈值
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

// recomputeHash 重算 tx hash：sha256(proto.Marshal(raw))。
func recomputeHash(t *testing.T, tx *core.Transaction) string {
	t.Helper()
	rawBytes, err := proto.Marshal(tx.GetRawData())
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	sum := sha256.Sum256(rawBytes)
	return hex.EncodeToString(sum[:])
}

// TestVote_TxHashRecomputedAfterMutation 红绿锚：变异 PermissionId 后返回的 hash
// 必须 ≠ txExt.GetTxid()（变异前 hash），证明修复必要。
// 修复前（返回 txExt.GetTxid()）→ 此测试 FAIL（hash == txid，未重算）。
// 修复后（返回重算 hash）→ 此测试 PASS（hash ≠ txid，已重算）。
func TestVote_TxHashRecomputedAfterMutation(t *testing.T) {
	txid := []byte{0xde, 0xad, 0xbe, 0xef, 0x12, 0x34, 0x56, 0x78}
	mc := &mockTronClient{voteTxExt: makeTxExt(txid)}
	e := NewExecutor(mc, testPrivKey(t), "Tfrom", nil)

	txHash, err := e.Vote(context.Background(), []VoteEntry{{SRAddress: "Tsr1", Votes: 5000}})
	if err != nil {
		t.Fatalf("Vote: %v", err)
	}
	origTxidHex := hex.EncodeToString(txid)
	if txHash == origTxidHex {
		t.Fatalf("txHash == txExt.GetTxid() (%q)，变异后未重算——F1 bug 仍在", txHash)
	}
	// 重算 hash 必须与独立重算结果一致（证明重算逻辑正确）
	want := recomputeHash(t, mc.voteTxExt.GetTransaction())
	if txHash != want {
		t.Errorf("txHash = %q, want recompute %q", txHash, want)
	}
}

// TestVote_PreMutationHashMatchesTxid 等式锚：变异前重算 hash == txExt.GetTxid()，
// 证明重算逻辑与 TRON txid 计算一致（sha256(raw_data)）。
// mock txid 必须构造为 sha256(raw_data) 才能验证等式。
func TestVote_PreMutationHashMatchesTxid(t *testing.T) {
	txExt := makeTxExt(nil) // txid 先空
	// 构造 txid = sha256(raw_data)（TRON txid 计算法）
	rawBytes, err := proto.Marshal(txExt.GetTransaction().GetRawData())
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	sum := sha256.Sum256(rawBytes)
	txExt.Txid = sum[:]
	origTxidHex := hex.EncodeToString(sum[:])
	// 变异前重算
	preHash := recomputeHash(t, txExt.GetTransaction())
	if preHash != origTxidHex {
		t.Fatalf("变异前重算 hash %q != txExt.GetTxid() %q——重算逻辑与 TRON txid 不一致", preHash, origTxidHex)
	}
	// 变异后重算（证明修复必要）
	txExt.GetTransaction().RawData.Contract[0].PermissionId = ArbPermissionId
	postHash := recomputeHash(t, txExt.GetTransaction())
	if postHash == origTxidHex {
		t.Fatal("变异后重算 hash == 原 txid——变异未影响 hash，修复无意义")
	}
}

// TestClaimReward_TxHashRecomputedAfterMutation 红绿锚：ClaimReward 同 Vote 修复。
func TestClaimReward_TxHashRecomputedAfterMutation(t *testing.T) {
	txid := []byte{0xca, 0xfe, 0x12, 0x34}
	mc := &mockTronClient{
		reward:        15_000_000,
		withdrawTxExt: makeTxExt(txid),
	}
	e := NewExecutor(mc, testPrivKey(t), "Tfrom", nil)

	txHash, _, err := e.ClaimReward(context.Background())
	if err != nil {
		t.Fatalf("ClaimReward: %v", err)
	}
	origTxidHex := hex.EncodeToString(txid)
	if txHash == origTxidHex {
		t.Fatalf("txHash == txExt.GetTxid() (%q)，变异后未重算——F1 bug 仍在", txHash)
	}
	want := recomputeHash(t, mc.withdrawTxExt.GetTransaction())
	if txHash != want {
		t.Errorf("txHash = %q, want recompute %q", txHash, want)
	}
}
