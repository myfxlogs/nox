// wire_health.go —— D-378a S2-S4：exec 注入/健康监控 helper 组自 setup.go 整组移出
// （同包纯搬移零行为变更；check-lines 300 行门禁修法，D-366 digest.go 先例）。
package main

import (
	"context"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/bus"
	"nox/internal/ledger/reconcile"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/platform/usdnotional"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

func wireExecInjections(pipeline *exec.Pipeline, st *pgstore.Store, recon *reconcile.Reconciler, cfg *config.Config, commResolver func(venueKey string) float64, qbus *bus.QuoteBus) {
	pipeline.SetPnLCalibrator(execPnLCalibrator{r: recon})
	pipeline.SetFeeBpsResolver(exec.FeeBpsResolver(commResolver))            // D-599 S2：净额核算估算费率链（同 commissionResolver）
	pipeline.SetContractSizeResolver(execCSResolver(pipelineCSResolver(st))) // D-311 R2：复合分派（:perp→ct_val/:mt4/:mt5→broker_symbol_map/:spot→1.0）
	pipeline.SetFxResolver(usdnotional.NewFxLive(qbus))                      // D-105 T2：平仓 PnL 计价币→USD 换算（D-633 A 档=qbus MT 键域）
	st.SetUSDPerNowFn(func(ctx context.Context, ccy, vk string) (float64, bool) {
		return usdnotional.USDPerNow(ctx, qbus, ccy, vk)
	}) // D-633 A 档：PositionNetUSD 现态 fx 注入
	pipeline.SetSwapArbComboAllow(swapArbAllowFromTableOnly(context.Background(), st), cfg.OpportunityNotionalUSD) // D-515 S5：表化同源（env 停读）
	pipeline.SetXAssetComboAllow(xassetAllowFromTable(context.Background(), st, cfg.Environment))                  // D-338 S3：同 slice 同源（manual 路径不分裂）
}

// setupMTHealth 构造 MT 数据采集健康监控（D-104 P0）：per-venue 三指标（报价/swap/margin 新鲜度）
// + 转换式告警状态机（防洪水）+ 每 tick 日志行。首 tick 只记状态不告警。
// 从 main.go 拆出（D-112 S5.2 文件行数治理），goroutine 启动留 main.go。
func setupMTHealth(brokerMgr *dashboard.BrokerManager, qbus *bus.QuoteBus, st *pgstore.Store, cfg *config.Config) *dashboard.MTHealthMonitor {
	return dashboard.NewMTHealthMonitor(brokerMgr, qbus, st, cfg.MTData.HealthTick,
		cfg.MTData.HealthQuoteWarn, cfg.MTData.HealthQuoteCrit, cfg.MTData.HealthFactWarn, cfg.MTData.HealthFactCrit,
		cfg.MTData.HealthMarginWarn, cfg.MTData.HealthMarginCrit) // D-067：margin 独立阈值
}

// setupTableHealth 构造表级新鲜度监控（D-301a S1）：8 表白名单 + 三态转换式告警状态机
// （照抄 mt-health 同族语义），tick=60s 固定。兜低频入库表暗死（fin_cmc_* 断流/funding_history 停滞）。
func setupTableHealth(st *pgstore.Store) *dashboard.TableFreshnessMonitor {
	return dashboard.NewTableFreshnessMonitor(st, dashboard.DefaultTableWatches, 60*time.Second)
}

// setupCexProxyHealth 构造 CEX 静态代理健康监控（D-301b S2）：透传 ping 探测（代理活+鉴权通+
// 上游可达一次验证），3 连败 crit+恢复 info。BINANCE_PROXY_URL 空 → nil（fail-open 不阻塞启动）。
func setupCexProxyHealth(st *pgstore.Store, cfg *config.Config) *dashboard.CexProxyHealthMonitor {
	return dashboard.NewCexProxyHealthMonitor(st, cfg.BinanceProxyURL, cfg.BinanceProxyURLBackup)
}

// setupEgressProbe 构造出口直连链路健康探针（D-011 S1）：三域直连探活+代理可达性 →
// 三域全灭且代理可达时单条聚合根因 critical（1h 去重）+恢复 info；per-domain 落 dephealth。
func setupEgressProbe(st *pgstore.Store, cfg *config.Config) *dashboard.EgressProbe {
	return dashboard.NewEgressProbe(st, cfg.BinanceProxyURL)
}

// startCexProxyHealth 启动 CEX 静态代理健康钉（BINANCE_PROXY_URL 空 → 不启动，fail-open）。
func startCexProxyHealth(ctx context.Context, st *pgstore.Store, cfg *config.Config) {
	if m := setupCexProxyHealth(st, cfg); m != nil {
		safego.GoLoop("main:cexProxyHealth", func() { m.Run(ctx) })
	}
}

// startEgressProbe 启动出口直连链路健康探针（main_monitors 6g4 挪入保 main_monitors ≤300 行）。
func startEgressProbe(ctx context.Context, st *pgstore.Store, cfg *config.Config) {
	egressProbe := setupEgressProbe(st, cfg)
	safego.GoLoop("main:egressProbe", func() { egressProbe.Run(ctx) })
}

// pulsedAwareFor D-334c S1/T3：demo 域脉动型 funding 品种均值语义开关（Environment 单源派生，零新 env）；real/未知恒 false=worst 严判一字不动。
func pulsedAwareFor(environment string) bool { return environment == "demo" }
