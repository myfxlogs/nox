// D-231 S2/S5 测试：trx_account Interval 断言（S2=1min→S5=30s）。
// D-137：trx_apr/trx_txs 常驻轮询摘除——apr 走边界事件 TriggerNow，txs 降 10min 低频对账。
package main

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/platform/config"
)

// TestWireTrx_AccountIntervalThirtySeconds 验证 trx_account 30s Interval 保留（D-137：
// 唯一外部变更检测器+reward 差分边界探测器本尊，审计否定降频）。
func TestWireTrx_AccountIntervalThirtySeconds(t *testing.T) {
	hedgeTopic := bus.NewTopic[struct{}]()
	cfg := &config.Config{EnvKnobs: config.EnvKnobs{TrxStakeAddress: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555"}}
	w := buildTrxCollectors(context.Background(), nil, hedgeTopic, bus.NewTopic[string](), cfg, config.NewSecretResolver(nil)) // st=nil 因不实际 Poll（resolveTronscanBase nil 安全回落 cfg/default）
	if w == nil {
		t.Fatal("buildTrxCollectors returned nil")
	}
	var accountInterval, txsInterval, aprInterval time.Duration
	var hasAccount, hasTxs bool
	for _, c := range w.sources {
		switch c.Name {
		case "trx_account":
			accountInterval = c.Interval
			hasAccount = true
		case "trx_apr":
			aprInterval = c.Interval // D-157：回 sources 1h failsafe（事件径 TriggerNow 不动）
		case "trx_txs":
			txsInterval = c.Interval
			hasTxs = true
		}
	}
	if !hasAccount {
		t.Fatal("trx_account collector not found in buildTrxCollectors output")
	}
	if accountInterval != 30*time.Second {
		t.Errorf("trx_account Interval = %v, want 30s (D-231 S5 提频，D-137 保留)", accountInterval)
	}
	if !hasTxs {
		t.Fatal("trx_txs collector not found（D-137⑦ 应保留为 10min 低频对账）")
	}
	if txsInterval != 10*time.Minute {
		t.Errorf("trx_txs Interval = %v, want 10m (D-137⑦ 低频对账)", txsInterval)
	}
	if aprInterval != time.Hour {
		t.Errorf("trx_apr Interval = %v, want 1h (D-157 failsafe 兜底；摘常驻轮询是 5min 档非全摘)", aprInterval)
	}
	// apr/txs 句柄返回供事件驱动接线
	if w.apr == nil || w.txs == nil || w.publishHedge == nil {
		t.Error("trxWiring 句柄不全（apr/txs/publishHedge 供边界事件接线）")
	}
}
