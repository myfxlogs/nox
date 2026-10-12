// D-054：TRX 投票优化器凭据解析锚——地址读 env 轨（secRes 域错配回归锚）+
// 装配降级 critical 告警。
package main

import (
	"context"
	"strings"
	"testing"

	"nox/internal/platform/config"
)

const d054Addr = "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555"

// TestWireTrxVote_AddrGateFromEnv 票③锚（D-145 改锚：cfg 有地址 + resolver 无私钥 →
// 地址闸通过——读自 cfg 字段，非 secRes/env）。
func TestWireTrxVote_AddrGateFromEnv(t *testing.T) {
	priv, addr, reason := trxVoteOptimizerCreds(config.NewSecretResolver(nil),
		&config.Config{EnvKnobs: config.EnvKnobs{TrxStakeAddress: d054Addr}})
	if addr != d054Addr {
		t.Fatalf("addr=%q 地址闸未过（secRes 域错配回归——应读 env）", addr)
	}
	if priv != "" || !strings.Contains(reason, "TRX_VOTE_PRIVATE_KEY") {
		t.Fatalf("priv=%q reason=%q（无私钥应停在私钥闸）", priv, reason)
	}
}

// TestWireTrxVote_CredsGates 三态：全通 / 缺地址 / 缺私钥。
func TestWireTrxVote_CredsGates(t *testing.T) {
	res := config.NewSecretResolver(nil)
	res.Set("TRX_VOTE_PRIVATE_KEY", "00")

	priv, addr, reason := trxVoteOptimizerCreds(res,
		&config.Config{EnvKnobs: config.EnvKnobs{TrxStakeAddress: d054Addr}})
	if priv != "00" || addr != d054Addr || reason != "" {
		t.Fatalf("全通态: priv=%q addr=%q reason=%q", priv, addr, reason)
	}

	_, addr, reason = trxVoteOptimizerCreds(res, &config.Config{})
	if addr != "" || !strings.Contains(reason, "TRX_STAKE_ADDRESS") {
		t.Fatalf("缺地址态: addr=%q reason=%q", addr, reason)
	}
}

// TestWireTrxVote_DegradeAlert 票②锚：凭据缺 → nil + critical alert 一条。
func TestWireTrxVote_DegradeAlert(t *testing.T) {
	var got []string
	sink := func(level, msg string) { got = append(got, level+"|"+msg) }
	// resolver 空（无私钥）→ 凭据闸前短路，不触 st/gRPC。
	sched := buildTrxVoteOptimizer(context.Background(), nil, &config.Config{},
		config.NewSecretResolver(nil), sink)
	if sched != nil {
		t.Fatal("缺凭据仍装配 sched")
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "critical|") ||
		!strings.Contains(got[0], "TRX_VOTE_PRIVATE_KEY") {
		t.Fatalf("alerts=%v（应 1 条 critical 降级告警）", got)
	}
	// nil sink 安全（装配面 sink 可能未接线）。
	_ = buildTrxVoteOptimizer(context.Background(), nil, &config.Config{},
		config.NewSecretResolver(nil), nil)
}
