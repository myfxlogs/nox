package main

import (
	"context"
	"log"
	"strings"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/bus"
	"nox/internal/data/collect"
	"nox/internal/data/collect/trx"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// trxWiring TRX 域采集器装配产物（D-137：事件驱动边界接线需要 apr/txs/publishHedge 句柄）。
type trxWiring struct {
	sources      []collect.Named
	account      *trx.AccountCollector
	apr          *trx.AprCollector
	txs          *trx.TxsCollector
	publishHedge func()
}

// buildTrxCollectors 组装 TRX 质押监控采集器（D-211，SSOT §4）。
// env：TRX_STAKE_ADDRESS（缺省跳过——优雅降级）/TRONGRID_API_KEY（空=匿名）/TRX_RENTAL_PAYERS（默认锁定值）。
// P2-1：返回 []collect.Named append 进既有 Scheduler sources——既有 Sink 收到空切片天然 no-op。
// D-231 S5：hedgeTopic 注入——account 指纹变化→apr.TriggerNow+hedgeTopic.Publish（≤30s+推送时延体现）。
// D-293 S2：返回 *trx.AccountCollector 供调用方注入 scheduler.ClaimCheck（构造顺序：collector 先于 scheduler）。
// D-296 S2：tronscanBase 读 DB system_settings → env TRONSCAN_BASE → DefaultTronscanBase（三级回落）。
// D-137④⑦：trx_apr 5min 常驻轮询摘除（边界事件→TriggerNow）；trx_txs 1min→10min 低频对账。
// D-157：trx_apr 回 sources 1h failsafe 兜底（事件径不动）+watchdog 15min→2.5h 口径修复。
// （自发 tx 走确认窗事件拉取，外部 rental payer 入账无流可订，低频 reconcile 兜底）。
func buildTrxCollectors(ctx context.Context, st *pgstore.Store, hedgeTopic *bus.Topic[struct{}], refreshTopic *bus.Topic[string], cfg *config.Config, secRes *config.SecretResolver) *trxWiring {
	addr := cfg.TrxStakeAddress
	if addr == "" {
		log.Println("[wiring] TRX_STAKE_ADDRESS 未设置，跳过 TRX 采集器")
		return nil
	}
	// D-545：TronGrid key 热读——UpdateAppSecret 写后 resolver.Set 即时生效（每请求现取）。
	apiKey := func() string { return secRes.Get("TRONGRID_API_KEY") }
	// TRX_RENTAL_PAYERS env 默认值=TBtvnRB（业主已定性=租金付款方，勿改勿删）
	rentalPayers := []string{"TBtvnRBgrVE9zZWkWfPvDSiuo65es55555"}
	if cfg.TrxRentalPayers != "" {
		rentalPayers = strings.Split(cfg.TrxRentalPayers, ",")
	}
	tronscanBase := resolveTronscanBase(ctx, st, cfg)
	account := trx.NewAccountCollector(addr, st, trx.DefaultBase, tronscanBase, apiKey)
	safego.GoLoop("wire_trx:accountWatchdog", func() { account.StartWatchdog(context.Background()) })
	// D-231 S1：trx_apr 投票收益率采集器（TRONSCAN /api/witness + /api/vote）。
	// D-137④：5min 常驻轮询摘除——边界事件（reward 差分/自 tx 确认/failsafe）经 TriggerNow 拉。
	apr := trx.NewAprCollector(addr, st, tronscanBase)
	safego.GoLoop("wire_trx:aprWatchdog", func() { apr.StartWatchdog(context.Background()) })
	// D-231 S5：hedgeTopic 接线——apr 聚合值变化→hedgeTopic.Publish；account 指纹变化→apr.TriggerNow+hedgeTopic.Publish。
	publishHedge := func() {
		hedgeTopic.Publish(struct{}{})
		refreshTopic.Publish("trx") // D-508 S9：TRX 域变更→case 14（TrxYieldRadar 等订阅重取）
	}
	apr.SetHedgeHook(publishHedge)
	account.SetOnChange(func() {
		apr.TriggerNow(context.Background())
		publishHedge() // account 变化也推（质押/余额/换票/奖励结算统一 ≤30s+推送时延体现）
	})
	txs := trx.NewTxsCollector(addr, st, trx.DefaultBase, apiKey, tronscanBase, rentalPayers, cfg.TrxEnergyRateFloorPct)
	sources := []collect.Named{
		// D-231 S5：trx_account 1min→30s（钱包手动质押/换票 ≤30s 检测窗，免自建节点下限）。
		// D-137：30s 保留不降——唯一外部变更检测器 + reward 差分边界探测器本尊。
		{Name: "trx_account", Interval: 30 * time.Second, Collector: account},
		// D-137⑦：trx_txs 1min→10min 低频对账（自发 tx 改走确认窗事件径；
		// rental payer 外部入账无流可订=tronscan REST 仅拉源，D-004 例外留低频 reconcile）。
		{Name: "trx_txs", Interval: 10 * time.Minute, Collector: txs},
		// D-157：trx_apr 1h failsafe 兜底——事件驱动边界径（TriggerNow）不动；
		// tronscan 无流可订+边界周期 ~6h，1h 低频保鲜让 stale 重新可判（sched 首轮即时 Poll 防 boot 空窗）。
		{Name: "trx_apr", Interval: time.Hour, Collector: apr},
		{Name: "trx_witness", Interval: 24 * time.Hour, Collector: trx.NewWitnessCollector(addr, st, trx.DefaultBase, apiKey)},
	}
	return &trxWiring{sources: sources, account: account, apr: apr, txs: txs, publishHedge: publishHedge}
}

// resolveTronscanBase 三级回落：DB system_settings.tronscan_base → env TRONSCAN_BASE → trx.DefaultTronscanBase。
// DB 读失败（表不存在/未迁移/st=nil）静默回落，不阻断启动（迁移后即生效）。
func resolveTronscanBase(ctx context.Context, st *pgstore.Store, cfg *config.Config) string {
	if st != nil {
		if sys, err := st.GetSystemSettings(ctx); err == nil && sys.TronscanBase != "" {
			return sys.TronscanBase
		}
	}
	if cfg.TronscanBase != "" {
		return cfg.TronscanBase
	}
	return trx.DefaultTronscanBase
}

// startTrxDailyRollup 启动 TRX 日终 rollup ticker（D-211 SSOT §5）。
// 每日 00:05 UTC 跑 RollupTrxDailyIncome(昨日)；boot 补跑昨日（若 source='auto' 缺失）。
// dashSvc nil = 未接线（测试场景，no-op）。
func startTrxDailyRollup(ctx context.Context, dashSvc *dashboard.Service) {
	if dashSvc == nil {
		return
	}
	// boot 补跑昨日（best-effort，失败仅 log）
	yesterday := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	if err := dashSvc.RollupTrxDailyIncome(ctx, yesterday); err != nil {
		log.Printf("[trx-rollup] boot backfill yesterday: %v", err)
	}
	// 每日 00:05 UTC ticker：计算到下一个 00:05 UTC 的间隔，然后 24h 周期
	safego.GoLoop("wire_trx:dailyRollup", func() {
		now := time.Now().UTC()
		next := now.Truncate(24 * time.Hour).Add(24*time.Hour + 5*time.Minute)
		timer := time.NewTimer(next.Sub(now))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		runTrxRollup(ctx, dashSvc)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runTrxRollup(ctx, dashSvc)
			}
		}
	})
}

// runTrxRollup 跑昨日 rollup（best-effort）。
func runTrxRollup(ctx context.Context, dashSvc *dashboard.Service) {
	yesterday := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	if err := dashSvc.RollupTrxDailyIncome(ctx, yesterday); err != nil {
		log.Printf("[trx-rollup] %v: %v", yesterday.Format("2006-01-02"), err)
	}
}
