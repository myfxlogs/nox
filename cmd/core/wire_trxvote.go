// D-269 S4：TRX 投票收益优化器装配（从 env 读取私钥+地址，构造 Scheduler）。
// SSOT: docs/D-269设计-TRX投票收益优化器-2026-09-10-Claude.md §4.2
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/portfolio/trxvote"
	"nox/internal/store/pgstore"

	"github.com/fbsobreira/gotron-sdk/pkg/client"
	"github.com/fbsobreira/gotron-sdk/pkg/keys"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// trxVoteOptimizerCreds 凭据解析（D-054①：TRX_STAKE_ADDRESS 是 app_config
// env-passthrough 键非密钥——secretResolverAllowed 白名单外 Get 恒 ""（D-564 起
// 优化器断流根因）；D-145：改读 cfg.TrxStakeAddress（env-passthrough 升 typed 字段）。
// 私钥保留 secRes 密钥轨。返回 (privKeyHex, addr, reason)：reason 非空=降级跳过因。
func trxVoteOptimizerCreds(secRes *config.SecretResolver, cfg *config.Config) (privKeyHex, addr, reason string) {
	privKeyHex = secRes.Get("TRX_VOTE_PRIVATE_KEY")
	addr = cfg.TrxStakeAddress
	switch {
	case privKeyHex == "":
		reason = "TRX_VOTE_PRIVATE_KEY 未设置"
	case addr == "":
		reason = "TRX_STAKE_ADDRESS 未设置"
	}
	return
}

// buildTrxVoteOptimizer 从 env 装配 TRX 投票收益优化器（设计 §4.2 v1.1）。
// 未设置私钥 → 优雅降级返回 nil（D-269 §4.2）。
// D-296 S2：tronscanBase 读 DB system_settings → env TRONSCAN_BASE → DefaultTronscanBase（删 tronscanapi.com 硬编码）。
// D-054②：任一装配降级（凭据缺/私钥坏/连接败）除 log 外落 critical alert——
// 「执行面装配降级」属应告警事件（boot 一次性，nil sink 安全）。
func buildTrxVoteOptimizer(ctx context.Context, st *pgstore.Store, cfg *config.Config, secRes *config.SecretResolver, alertSink func(string, string)) *trxvote.Scheduler {
	// D-564 S5 U7：凭据读改 SecretResolver（DB∪env 种子热读——env 直读退役，门禁硬拒域）。
	degrade := func(msg string) *trxvote.Scheduler {
		log.Println(msg)
		if alertSink != nil {
			alertSink("critical", msg)
		}
		return nil
	}
	privKeyHex, addr, credWhy := trxVoteOptimizerCreds(secRes, cfg)
	if credWhy != "" {
		return degrade("[wiring] TRX 投票优化器装配降级：" + credWhy + "，跳过（自动改投/自动领奖不启）")
	}

	// 解析私钥（never log the key itself，v1.1 B2：keys.GetPrivateKeyFromHex）
	privKey, err := keys.GetPrivateKeyFromHex(privKeyHex)
	if err != nil {
		return degrade(fmt.Sprintf("[wiring] TRX_VOTE_PRIVATE_KEY 解析失败: %v（投票优化器不启）", err))
	}

	// gRPC 连接 TronGrid
	grpcEndpoint := cfg.TrxGrpcEndpoint
	if grpcEndpoint == "" {
		grpcEndpoint = "grpc.trongrid.io:50051"
	}
	conn := client.NewGrpcClient(grpcEndpoint)
	if err := conn.Start(grpc.WithTransportCredentials(insecure.NewCredentials())); err != nil {
		return degrade(fmt.Sprintf("[wiring] TRX gRPC 连接失败: %v（投票优化器不启）", err))
	}
	// TronGrid API key（复用 TRONGRID_API_KEY，提升限流 3→100 RPS；空=匿名降级）
	if apiKey := secRes.Get("TRONGRID_API_KEY"); apiKey != "" {
		conn.SetAPIKey(apiKey)
	}

	// 构造 Executor + Scheduler
	executor := trxvote.NewExecutor(conn, privKey, addr, alertSink)
	storeAdapter := &trxvote.PgStoreAdapter{Store: st}

	tronscanBase := resolveTronscanBase(ctx, st, cfg)

	rebalanceCfg := trxvote.RebalanceConfig{
		MinSRs:             cfg.TrxVoteMinSRs,
		MaxSRs:             cfg.TrxVoteMaxSRs,
		MaxBrokerage:       cfg.TrxVoteMaxBrokerage,
		MinRank:            127, // top 127 都可
		YieldDropPct:       cfg.TrxVoteYieldDropPct,
		BetterOppPct:       cfg.TrxVoteBetterOppPct,
		MaxRebalancePerDay: cfg.TrxVoteMaxRebalPerDay,
		MinInterval:        time.Duration(cfg.TrxVoteMinIntervalMin) * time.Minute, // D-293 S3：防抖（env 默认 120min=2h）
		SpreadThreshold:    cfg.TrxVoteSpreadThreshold,                             // D-297：价差触发改投阈值
	}

	// D-117：账户投票读路改 tronscan /api/vote 主读 + trongrid gRPC 兜底——
	// gRPC GetAccount 对 SG IP 持续性 429（17:59Z 起 13h+，带 key 仍限），
	// tronscan HTTP 面（witness/account collector）同链数据全程正常。
	sched := trxvote.NewScheduler(executor, storeAdapter, addr, tronscanBase, rebalanceCfg,
		trxvote.NewFallbackAccountFetcher(
			trxvote.NewTronscanAccountFetcher(tronscanBase),
			trxvote.NewGRPCAccountFetcher(conn)))
	// D-137⑤：failsafe/boot 补课用 nextMaintenance 缓存源（gRPC 单次调用，非轮询）。
	sched.SetNextMaintenanceSource(func(ctx context.Context) (time.Time, error) {
		nm, err := conn.GetNextMaintenanceTimeCtx(ctx)
		if err != nil {
			return time.Time{}, err
		}
		return time.UnixMilli(nm.GetNum()), nil
	})
	return sched
}

// startTrxVoteOptimizer 启动投票优化器（nil 安全）+ D-137 事件驱动边界接线。
// D-293 S2：注入 ClaimCheck 到 account collector（领奖搭车 30s 采集流，piggyback 语义零改动）。
// D-137①④：reward 差分 → KickBoundary；评估完成 → apr.TriggerNow+publishHedge；
// 自发 tx confirmed → account/apr/txs 重取。
func startTrxVoteOptimizer(sched *trxvote.Scheduler, w *trxWiring, ctx context.Context) {
	if w != nil && w.account != nil {
		w.account.SetOnRewardDelta(func(ctx context.Context, rewardNumSun, delta int64) {
			log.Printf("[trxvote] reward delta %+d sun (→%d) = maintenance boundary → kick", delta, rewardNumSun)
			if sched != nil {
				sched.KickBoundary("reward_delta")
			}
			if w.apr != nil {
				w.apr.TriggerNow(ctx)
			}
			w.publishHedge()
		})
	}
	if sched == nil {
		return // 优雅降级
	}
	if w != nil && w.account != nil {
		w.account.SetOnAccountFetched(sched.ClaimCheck)
	}
	if w != nil {
		sched.SetOnEvaluated(func(ctx context.Context) {
			if w.apr != nil {
				w.apr.TriggerNow(ctx) // D-137④：边界评估后 APR 重取
			}
			w.publishHedge()
		})
		sched.SetOnSelfTxConfirmed(func(ctx context.Context) {
			_, _ = w.account.Poll(ctx) // D-137③：自发 tx confirmed → account 重取
			if w.apr != nil {
				w.apr.TriggerNow(ctx)
			}
			_, _ = w.txs.Poll(ctx) // 自发 tx 即刻入账（不等 10min 对账）
			w.publishHedge()
		})
	}
	safego.Go("main:trxvote", func() {
		sched.Run(ctx)
	})
}
