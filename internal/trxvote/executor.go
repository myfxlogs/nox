// D-269 S3：TRX 投票交易执行器（gotron-sdk v0.26.0 集成）。
// 实现 Vote（改投）+ ClaimReward（领奖），私钥从 env 读取 never log。
// SSOT: docs/D-269设计-TRX投票收益优化器-2026-09-10-Claude.md §4
package trxvote

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/fbsobreira/gotron-sdk/pkg/client"
	"github.com/fbsobreira/gotron-sdk/pkg/client/transaction"
	"github.com/fbsobreira/gotron-sdk/pkg/proto/api"
	"github.com/fbsobreira/gotron-sdk/pkg/proto/core"
)

// BandwidthNeedVoteBP 改投所需带宽（BP）——D-269 预检+D-278 S5 资源显示同源引用（禁双写）。
const BandwidthNeedVoteBP int64 = 500

// BandwidthNeedClaimBP 领奖所需带宽（BP）——D-269 预检+D-278 S5 资源显示同源引用（禁双写）。
const BandwidthNeedClaimBP int64 = 250

// ArbPermissionId 多签账户 arb 权限 ID（active[1]，threshold=1 单签）。
// TWsbXR6 多签后 owner threshold=2 需双签，arb 权限单签可执行 Vote+WithdrawBalance。
// gotron-sdk 不设 PermissionId 默认=0(owner)，单签广播报 "sig error"。
const ArbPermissionId int32 = 3

// TronClient 是 Executor 依赖的 TRON gRPC 客户端接口（便于 mock 测试）。
type TronClient interface {
	VoteWitnessAccountCtx(ctx context.Context, from string, witnessMap map[string]int64) (*api.TransactionExtention, error)
	WithdrawBalanceCtx(ctx context.Context, from string) (*api.TransactionExtention, error)
	GetRewardsInfoCtx(ctx context.Context, addr string) (int64, error)
	BroadcastCtx(ctx context.Context, tx *core.Transaction) (*api.Return, error)
	GetAccountResourceCtx(ctx context.Context, addr string) (*api.AccountResourceMessage, error)
}

// Executor 执行 TRX 投票交易（改投+领奖）。
type Executor struct {
	conn      TronClient
	privKey   *btcec.PrivateKey       // 从 env 读取，never log
	fromAddr  string                  // base58
	alertSink func(level, msg string) // D-268 AlertSink 复用（可为 nil）
}

// NewExecutor 构造执行器。
func NewExecutor(conn TronClient, privKey *btcec.PrivateKey, fromAddr string, alertSink func(level, msg string)) *Executor {
	return &Executor{
		conn:      conn,
		privKey:   privKey,
		fromAddr:  fromAddr,
		alertSink: alertSink,
	}
}

// Vote 执行改投（覆盖式——每次提交全部投票分配，设计 §4.1）。
// 流程：构建未签名 tx → 客户端签名 → 广播 → 返回 txHash。
func (e *Executor) Vote(ctx context.Context, target []VoteEntry) (string, error) {
	witnessMap := make(map[string]int64, len(target))
	for _, v := range target {
		witnessMap[v.SRAddress] = int64(v.Votes) // TRX 单位（链上 vote_count = TRX）
	}

	// 1. 构建未签名交易（v1.1 B4：用 GetTransaction() 取 *core.Transaction）
	txExt, err := e.conn.VoteWitnessAccountCtx(ctx, e.fromAddr, witnessMap)
	if err != nil {
		return "", fmt.Errorf("build vote tx: %w", err)
	}
	if txExt == nil || txExt.GetTransaction() == nil {
		return "", fmt.Errorf("build vote tx: nil transaction returned")
	}

	// 多签账户：设 arb 权限（permission_id=3，threshold=1 单签），默认 owner=0 需双签。
	txExt.GetTransaction().RawData.Contract[0].PermissionId = ArbPermissionId

	// 2. 客户端签名（私钥不出本进程）
	signedTx, err := transaction.SignTransaction(txExt.GetTransaction(), e.privKey)
	if err != nil {
		return "", fmt.Errorf("sign vote tx: %w", err)
	}

	// 3. 广播（v1.1 B3：Broadcast 返回 *api.Return 无 Txid，txHash 从 txExt.GetTxid() 取）
	if _, err := e.conn.BroadcastCtx(ctx, signedTx); err != nil {
		return "", fmt.Errorf("broadcast vote tx: %w", err)
	}

	return hex.EncodeToString(txExt.GetTxid()), nil
}

// ClaimReward 领取投票奖励（设计 §4.1）。
// 流程：查未领取奖励 → 构建 withdraw tx → 签名 → 广播 → 返回 txHash+amount。
// reward < 10 TRX（10_000_000 sun）→ 跳过（节省带宽，业主 9-11 定）。
func (e *Executor) ClaimReward(ctx context.Context) (string, int64, error) {
	// 1. 查未领取奖励（v1.1 B1：GetReward → GetRewardsInfoCtx）
	reward, err := e.conn.GetRewardsInfoCtx(ctx, e.fromAddr)
	if err != nil {
		return "", 0, fmt.Errorf("get reward: %w", err)
	}
	if reward < 10_000_000 {
		return "", 0, nil // 奖励不足 10 TRX，跳过节省带宽
	}

	// 2. 构建 withdrawbalance 交易
	txExt, err := e.conn.WithdrawBalanceCtx(ctx, e.fromAddr)
	if err != nil {
		return "", 0, fmt.Errorf("build withdraw tx: %w", err)
	}
	if txExt == nil || txExt.GetTransaction() == nil {
		return "", 0, fmt.Errorf("build withdraw tx: nil transaction returned")
	}

	// 多签账户：设 arb 权限（permission_id=3，threshold=1 单签），默认 owner=0 需双签。
	txExt.GetTransaction().RawData.Contract[0].PermissionId = ArbPermissionId

	// 3. 签名+广播
	signedTx, err := transaction.SignTransaction(txExt.GetTransaction(), e.privKey)
	if err != nil {
		return "", 0, fmt.Errorf("sign withdraw tx: %w", err)
	}
	if _, err := e.conn.BroadcastCtx(ctx, signedTx); err != nil {
		return "", 0, fmt.Errorf("broadcast withdraw tx: %w", err)
	}

	return hex.EncodeToString(txExt.GetTxid()), reward, nil
}

// Alert 发送告警（封装 alertSink nil 安全）。
func (e *Executor) Alert(level, msg string) {
	if e.alertSink != nil {
		e.alertSink(level, msg)
	}
}

// HasBandwidth 检查账户剩余带宽是否 >= need BP。
// 剩余 = (FreeNetLimit - FreeNetUsed) + (NetLimit - NetUsed)。
func (e *Executor) HasBandwidth(ctx context.Context, need int64) (bool, error) {
	res, err := e.conn.GetAccountResourceCtx(ctx, e.fromAddr)
	if err != nil {
		return false, fmt.Errorf("get account resource: %w", err)
	}
	free := res.GetFreeNetLimit() - res.GetFreeNetUsed()
	staked := res.GetNetLimit() - res.GetNetUsed()
	remaining := free + staked
	return remaining >= need, nil
}

// compile-time: *client.GrpcClient 实现 TronClient 接口。
var _ TronClient = (*client.GrpcClient)(nil)
