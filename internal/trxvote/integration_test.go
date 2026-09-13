// D-269 S5 集成测试：决策→执行→落库→告警全链路（httptest + mock gRPC + fake store）。
package trxvote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/fbsobreira/gotron-sdk/pkg/account"
	"github.com/fbsobreira/gotron-sdk/pkg/proto/api"
	"github.com/fbsobreira/gotron-sdk/pkg/proto/core"
)

// integrationStore 是集成测试用的 fake store（带状态追踪）。
type integrationStore struct {
	rebalancedToday bool
	insertedCount   int
	lastRecord      *TRXVoteRebalanceRecord
	updatedID       int64
	updatedTxHash   string
	updatedStatus   string
	// D-293 S3：防抖查询 mock
	lastExecutedAt time.Time
}

func (s *integrationStore) InsertTrxVoteRebalance(_ context.Context, rec TRXVoteRebalanceRecord) (int64, error) {
	s.insertedCount++
	s.lastRecord = &rec
	return int64(s.insertedCount), nil
}
func (s *integrationStore) UpdateTrxVoteRebalanceResult(_ context.Context, id int64, txHash, status string) error {
	s.updatedID = id
	s.updatedTxHash = txHash
	s.updatedStatus = status
	return nil
}
func (s *integrationStore) TrxVoteRebalancedToday(_ context.Context, _ int) bool {
	return s.rebalancedToday
}
func (s *integrationStore) LastExecutedRebalanceAt(_ context.Context) (time.Time, error) {
	return s.lastExecutedAt, nil
}

// integrationConn 是集成测试用的 mock TronClient。
type integrationConn struct {
	voteTxExt     *api.TransactionExtention
	reward        int64
	withdrawTxExt *api.TransactionExtention
	broadcastErr  error
}

func (c *integrationConn) VoteWitnessAccountCtx(_ context.Context, _ string, _ map[string]int64) (*api.TransactionExtention, error) {
	return c.voteTxExt, nil
}
func (c *integrationConn) WithdrawBalanceCtx(_ context.Context, _ string) (*api.TransactionExtention, error) {
	return c.withdrawTxExt, nil
}
func (c *integrationConn) GetRewardsInfoCtx(_ context.Context, _ string) (int64, error) {
	return c.reward, nil
}
func (c *integrationConn) BroadcastCtx(_ context.Context, _ *core.Transaction) (*api.Return, error) {
	return &api.Return{}, c.broadcastErr
}
func (c *integrationConn) GetAccountResourceCtx(_ context.Context, _ string) (*api.AccountResourceMessage, error) {
	return &api.AccountResourceMessage{FreeNetLimit: 600, NetLimit: 1285}, nil // 充足带宽
}

// integrationAccount 是集成测试用的 mock AccountFetcher。
type integrationAccount struct {
	acc *account.Account
}

func (a *integrationAccount) GetAccountDetailedCtx(_ context.Context, _ string) (*account.Account, error) {
	return a.acc, nil
}

// makeIntegrationScheduler 构造集成测试用 Scheduler（httptest TronScan + mock gRPC + fake store）。
func makeIntegrationScheduler(t *testing.T, srFixture string, store *integrationStore, conn *integrationConn, acc *account.Account) (*Scheduler, *Executor, *httptest.Server) {
	t.Helper()
	// httptest mock TronScan /api/witness
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(srFixture))
	}))
	SetHTTPClient(srv.Client())
	t.Cleanup(func() {
		SetHTTPClient(&http.Client{})
		srv.Close()
	})

	// 确定性私钥（非全零）
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 1)
	}
	privKey, _ := btcec.PrivKeyFromBytes(keyBytes)

	var alerts []string
	alertSink := func(level, msg string) {
		alerts = append(alerts, level+":"+msg)
	}

	executor := NewExecutor(conn, privKey, "Tfrom", alertSink)
	accFetcher := &integrationAccount{acc: acc}
	sched := NewScheduler(executor, store, "Tfrom", srv.URL, DefaultConfig(), accFetcher)

	// 把 alerts 暴露给测试（通过 closure）
	t.Cleanup(func() { _ = alerts })
	return sched, executor, srv
}

// TestIntegration_RebalanceExecute 决策→执行→落库→告警全链路（NeedRebalance=true）。
func TestIntegration_RebalanceExecute(t *testing.T) {
	// TronScan fixture：低票 SR（高 yield）+ 高票 SR（低 yield）
	srFixture := `[
		{"address":"SRgood","name":"SR1","annualizedRate":"5.0","brokerage":0,"realTimeVotes":100000,"votes":100000,"producer":true,"producedTotal":100},
		{"address":"SRbad","name":"SR2","annualizedRate":"1.0","brokerage":0,"realTimeVotes":10000000,"votes":10000000,"producer":true,"producedTotal":100}
	]`
	store := &integrationStore{rebalancedToday: false}
	txid := []byte{0xde, 0xad, 0xbe, 0xef, 0x12, 0x34, 0x56, 0x78}
	conn := &integrationConn{
		voteTxExt: makeTxExt(txid),
	}
	// 当前投 SRbad（高票低 yield）→ 目标选 SRgood（低票高 yield）→ NeedRebalance=true
	acc := &account.Account{
		TronPower: 10000,
		Votes:     map[string]int64{"SRbad": 10000},
	}

	sched, exec, _ := makeIntegrationScheduler(t, srFixture, store, conn, acc)
	var gotAlert string
	exec.alertSink = func(level, msg string) {
		gotAlert = level + ":" + msg
	}

	sched.doRebalance(context.Background())

	// 验证落库
	if store.insertedCount != 1 {
		t.Errorf("insertedCount = %d, want 1", store.insertedCount)
	}
	if store.lastRecord == nil || !store.lastRecord.NeedRebalance {
		t.Error("lastRecord.NeedRebalance should be true")
	}
	// 验证执行结果回填
	if store.updatedStatus != "executed" {
		t.Errorf("updatedStatus = %q, want executed", store.updatedStatus)
	}
	// D-293 S1：返回重算 hash（变异后），非 txExt.GetTxid()（变异前 deadbeef12345678）
	wantHash := recomputeHash(t, conn.voteTxExt.GetTransaction())
	if store.updatedTxHash != wantHash {
		t.Errorf("updatedTxHash = %q, want %q (recomputed)", store.updatedTxHash, wantHash)
	}
	// 验证告警
	if gotAlert == "" {
		t.Error("should alert on rebalance execute")
	}
	if len(gotAlert) < 4 || gotAlert[:4] != "warn" {
		t.Errorf("alert level = %q, want warn", gotAlert)
	}
}

// TestIntegration_RebalanceNoExecute 决策→不执行→落库→无告警（NeedRebalance=false）。
func TestIntegration_RebalanceNoExecute(t *testing.T) {
	srFixture := `[
		{"address":"SRgood","name":"SR1","annualizedRate":"5.0","brokerage":0,"realTimeVotes":100000,"votes":100000,"producer":true,"producedTotal":100},
		{"address":"SRbad","name":"SR2","annualizedRate":"1.0","brokerage":0,"realTimeVotes":10000000,"votes":10000000,"producer":true,"producedTotal":100}
	]`
	store := &integrationStore{rebalancedToday: false}
	conn := &integrationConn{}
	// 当前已等权投两个 SR（与目标一致）→ NeedRebalance=false
	// 注意：DefaultConfig MinSRs=3 但只有 2 个候选 → n=2，等权分配 5000 TRX each
	// acc.Votes 单位是 TRX（链上 vote_count=TRX）
	acc := &account.Account{
		TronPower: 10000,
		Votes:     map[string]int64{"SRgood": 5000, "SRbad": 5000},
	}

	sched, exec, _ := makeIntegrationScheduler(t, srFixture, store, conn, acc)
	var alertCalled bool
	exec.alertSink = func(level, msg string) {
		alertCalled = true
	}

	sched.doRebalance(context.Background())

	// 验证落库（无论是否执行都落库）
	if store.insertedCount != 1 {
		t.Errorf("insertedCount = %d, want 1", store.insertedCount)
	}
	if store.lastRecord == nil {
		t.Fatal("lastRecord should not be nil")
	}
	// 当前等权投两 SR（与目标一致）→ NeedRebalance=false
	if store.lastRecord.NeedRebalance {
		t.Errorf("NeedRebalance = true, want false (当前已是最优); improvement=%.2f", store.lastRecord.ImprovementPct)
	}
	// 验证不执行（无 UpdateTrxVoteRebalanceResult 调用）
	if store.updatedStatus != "" {
		t.Errorf("updatedStatus = %q, want empty (no execute)", store.updatedStatus)
	}
	// 验证无告警
	if alertCalled {
		t.Error("should not alert when no rebalance needed")
	}
}

// TestIntegration_RebalanceExecuteFailed 执行失败→落库→critical 告警。
func TestIntegration_RebalanceExecuteFailed(t *testing.T) {
	srFixture := `[
		{"address":"SRgood","name":"SR1","annualizedRate":"5.0","brokerage":0,"realTimeVotes":100000,"votes":100000,"producer":true,"producedTotal":100},
		{"address":"SRbad","name":"SR2","annualizedRate":"1.0","brokerage":0,"realTimeVotes":10000000,"votes":10000000,"producer":true,"producedTotal":100}
	]`
	store := &integrationStore{rebalancedToday: false}
	// Vote 返回 nil transaction → 构建失败
	conn := &integrationConn{
		voteTxExt: &api.TransactionExtention{Transaction: nil},
	}
	acc := &account.Account{
		TronPower: 10000,
		Votes:     map[string]int64{"SRbad": 10000},
	}

	sched, exec, _ := makeIntegrationScheduler(t, srFixture, store, conn, acc)
	var gotAlert string
	exec.alertSink = func(level, msg string) {
		gotAlert = level + ":" + msg
	}

	sched.doRebalance(context.Background())

	// 验证落库（决策记录仍落库）
	if store.insertedCount != 1 {
		t.Errorf("insertedCount = %d, want 1", store.insertedCount)
	}
	// 验证执行失败 → critical 告警
	if gotAlert == "" {
		t.Fatal("should alert on execute failure")
	}
	if len(gotAlert) < 8 || gotAlert[:8] != "critical" {
		t.Errorf("alert level = %q, want critical", gotAlert)
	}
	// 验证不回填 executed（执行失败）
	if store.updatedStatus == "executed" {
		t.Error("updatedStatus should not be executed on failure")
	}
}
