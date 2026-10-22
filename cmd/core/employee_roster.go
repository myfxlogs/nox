// employee_roster.go —— D-190：运行面 daemon 台账装配（编制=此处显式声明，禁自我声明漂移）。
// role/dept/assembly 在册即裁决留档；cadence_sec=0 常驻型以 running 判生死，
// >0 节拍型超 3×cadence 未心跳=滞跳（心跳写经 safego OnBeat/OnExit 钩，
// 挂现有 loop 节拍零新 goroutine）。
// D-246 v3 §4.4：Dept 收七标签（数据/研究/投资/风控/交易/账本/平台）；Beat 只 UPDATE
// 已注册名，未登记心跳转聚合 warn 告警不落行；WS ping/keepalive/子循环走
// GoPlain/GoLoopPlain 不入册。
package main

import (
	"context"
	"log"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/bus"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// employeeRoster 编制表（Name 必须与 safego.Go/GoLoop 实参一致；子节拍名不入册）。
var employeeRoster = []pgstore.Employee{
	// 数据域
	{Name: "main:cexscan", Role: "CEX 扫描", Dept: "数据", Assembly: "wire_cexscan.go"},
	{Name: "main:cex-io", Role: "CEX IO", Dept: "数据", Assembly: "wire_cex_io.go"},
	{Name: "main:macro", Role: "宏观日历", Dept: "数据", Assembly: "wire_macro.go"},
	{Name: "macro:calendar", Role: "宏观日历循环", Dept: "数据", Assembly: "wire_macro.go"},
	{Name: "macro:actuals", Role: "宏观实绩循环", Dept: "数据", Assembly: "wire_macro.go"},
	{Name: "main:ticker", Role: "行情", Dept: "数据", Assembly: "wire_quotes.go"},
	{Name: "main:tradifi-syms", Role: "TRADIFI 品种枚举", Dept: "数据", Assembly: "wire_cexscan.go"},
	{Name: "main:watchlist-provider", Role: "自选行情", Dept: "数据", Assembly: "wire_dashboard.go"},
	{Name: "main:funding-done-bridge", Role: "funding 桥", Dept: "数据", Assembly: "wire_quotes.go"},
	{Name: "main:gate-ws", Role: "Gate WS", Dept: "数据", Assembly: "wire_gate.go"},
	{Name: "main:binance-mkt-ws", Role: "币安行情 WS", Dept: "数据", Assembly: "wire_quotes.go"},
	{Name: "main:okx-ws", Role: "OKX WS", Dept: "数据", Assembly: "wire_quotes.go"},
	{Name: "main:cexStreamMgr", Role: "CEX 流管理", Dept: "数据", Assembly: "main_monitors.go"},
	{Name: "cex-stream:binance", Role: "币安私域流", Dept: "数据", Assembly: "cex_stream_manager.go"},
	{Name: "cex-stream:gate", Role: "Gate 私域流", Dept: "数据", Assembly: "cex_stream_manager.go"},
	{Name: "cex-stream:okx", Role: "OKX 私域流", Dept: "数据", Assembly: "cex_stream_manager.go"},
	{Name: "main:quoteGuard", Role: "行情闸门", Dept: "数据", Assembly: "main.go"},
	{Name: "setup:mt5-quote-fwd", Role: "MT5 行情转发", Dept: "数据", Assembly: "setup.go"},
	{Name: "mt5:marginSource", Role: "MT5 保证金源", Dept: "数据", Assembly: "mt5.go"},
	{Name: "mt5:orderEventSource", Role: "MT5 订单事件源", Dept: "数据", Assembly: "mt5.go"},
	{Name: "mt5:quoteStream", Role: "MT5 行情流", Dept: "数据", Assembly: "mt5.go"},
	{Name: "mt5:zeroframeWatchdog", Role: "MT5 零帧看门狗", Dept: "数据", Assembly: "mt_quote_stream.go"},
	{Name: "broker_manager_adapters:mt5-quote-pump", Role: "MT5 行情泵", Dept: "数据", Assembly: "broker_manager_adapters.go"},
	{Name: "tick_sampler:sample", Role: "tick 采样", Dept: "数据", Assembly: "tick_sampler.go"},
	// 研究域
	{Name: "main:explorer", Role: "机会探索", Dept: "研究", Assembly: "wire_explorer.go"},
	{Name: "main:oppmatch", Role: "提案扫描", Dept: "投资", Assembly: "wire_oppmatch.go", CadenceSec: 90000},
	{Name: "main:carry-universe", Role: "毕业环（仲裁批经 scan.Run→graduate）", Dept: "投资", Assembly: "wire_scan.go"},
	{Name: "main:idleCapital", Role: "闲置资金", Dept: "研究", Assembly: "main_monitors.go"},
	{Name: "sleep_money_mon", Role: "睡钱触发", Dept: "研究", Assembly: "wire_sleepmon.go"},
	{Name: "main:probeAdjudicator", Role: "探针裁决", Dept: "研究", Assembly: "main_monitors.go"},
	// 风控域
	{Name: "main:selfwatch", Role: "自监控", Dept: "风控", Assembly: "wire_selfwatch.go"},
	{Name: "selfwatch:anomaly", Role: "异常检测", Dept: "风控", Assembly: "selfwatch.go"},
	{Name: "selfwatch:canary", Role: "金丝雀", Dept: "风控", Assembly: "selfwatch.go"},
	{Name: "selfwatch:daily", Role: "日自检", Dept: "风控", Assembly: "selfwatch.go"},
	{Name: "main:acctMon", Role: "账户监控", Dept: "风控", Assembly: "main_monitors.go"},
	{Name: "main:acctFrameSnapshots", Role: "账户快照", Dept: "风控", Assembly: "main_monitors.go"},
	{Name: "main:mtHealth", Role: "MT 健康", Dept: "风控", Assembly: "main_monitors.go"},
	{Name: "main:persMon", Role: "个人持仓监控", Dept: "风控", Assembly: "main.go"},
	{Name: "main:bpMon.Run", Role: "BP 监控", Dept: "风控", Assembly: "main_monitors_bpmon.go"},
	// 交易域
	{Name: "main:eng", Role: "交易引擎", Dept: "交易", Assembly: "main.go"},
	{Name: "engine:oppLoop", Role: "引擎机会循环", Dept: "交易", Assembly: "engine.go"},
	{Name: "engine:meanRevertLoop", Role: "均值回归循环", Dept: "交易", Assembly: "engine.go"},
	{Name: "engine:swapArbLoop", Role: "换仓套利循环", Dept: "交易", Assembly: "engine.go"},
	{Name: "engine:quoteKickDispatch", Role: "行情触发派发", Dept: "交易", Assembly: "engine.go"},
	{Name: "engine:positionLoop", Role: "持仓循环", Dept: "交易", Assembly: "engine.go"},
	{Name: "engine:exitEvalWatchdog", Role: "平仓评估看门狗", Dept: "交易", Assembly: "engine.go"},
	{Name: "main:posMon", Role: "持仓监控", Dept: "交易", Assembly: "main.go"},
	{Name: "main:cexExecMgr", Role: "CEX 执行管理", Dept: "交易", Assembly: "main_monitors.go"},
	{Name: "main:cexPosMgr", Role: "CEX 持仓管理", Dept: "交易", Assembly: "main_monitors.go"},
	{Name: "main:bpMon.orderKick", Role: "BP 补单", Dept: "交易", Assembly: "main_monitors_bpmon.go"},
	{Name: "main_helpers:disabledStrategiesListener", Role: "淘汰监听", Dept: "交易", Assembly: "main_helpers.go"},
	{Name: "setup:atEngine.orderKick", Role: "引擎补单", Dept: "交易", Assembly: "setup.go"},
	{Name: "setup:atEngine.factsKick", Role: "引擎事实触发", Dept: "交易", Assembly: "setup.go"},
	// 账本域
	{Name: "main:income", Role: "收益采集", Dept: "账本", Assembly: "wire_income.go"},
	{Name: "main:okx_income", Role: "OKX 收益采集", Dept: "账本", Assembly: "wire_income.go"},
	{Name: "main:decision-outcomes", Role: "裁定归因簿（D-561）", Dept: "账本", Assembly: "wire_decision_outcomes.go", CadenceSec: 1800},
	{Name: "main:recon", Role: "对账", Dept: "账本", Assembly: "wire_posmon.go"},
	{Name: "main:dci-pool-reconcile", Role: "DCI 池对账", Dept: "账本", Assembly: "dci_go.go"},
	{Name: "setup:swapReconciler", Role: "swap 对账", Dept: "账本", Assembly: "setup.go"},
	{Name: "setup:swapLedger", Role: "swap 账本", Dept: "账本", Assembly: "setup.go"},
	{Name: "main:trxvote", Role: "TRX 投票", Dept: "账本", Assembly: "wire_trx.go", EventDriven: true},
	{Name: "trxvote:rebalance", Role: "TRX 调仓", Dept: "账本", Assembly: "trxvote.go"},
	{Name: "trxvote:confirmSelfTx", Role: "TRX 交易确认", Dept: "账本", Assembly: "trxvote.go", EventDriven: true},
	{Name: "wire_trx:aprWatchdog", Role: "TRX APR 看门狗", Dept: "账本", Assembly: "wire_trx.go"},
	{Name: "wire_trx:accountWatchdog", Role: "TRX 账户看门狗", Dept: "账本", Assembly: "wire_trx.go"},
	{Name: "wire_trx:dailyRollup", Role: "TRX 日汇总", Dept: "账本", Assembly: "wire_trx.go"},
	{Name: "main:sysretroDaily", Role: "日复盘", Dept: "账本", Assembly: "main_monitors.go"},
	{Name: "main:reviewRun", Role: "复盘触发", Dept: "账本", Assembly: "main.go", EventDriven: true},
	{Name: "main:posreview", Role: "复盘员判决书", Dept: "账本", Assembly: "wire_posreview.go"},
	{Name: "main:sysretroWeekly", Role: "周复盘", Dept: "账本", Assembly: "main_monitors.go"},
	// 平台域（横切）
	{Name: "main:sched", Role: "调度", Dept: "平台", Assembly: "main.go"},
	{Name: "sched:runSource", Role: "调度源循环", Dept: "平台", Assembly: "sched.go"},
	{Name: "main:feishu-push", Role: "飞书推送", Dept: "平台", Assembly: "wire_feishu.go"},
	{Name: "main:alertFanoutSweep", Role: "告警扇出扫带", Dept: "运维", Assembly: "wire_alert_sweep.go"},
	{Name: "feishu_query:handle", Role: "飞书查询处理", Dept: "平台", Assembly: "feishu_query.go", EventDriven: true},
	{Name: "main:deptreport", Role: "公司日报", Dept: "平台", Assembly: "wire_dashboard.go", CadenceSec: 86400},
	{Name: "main:kbReport", Role: "KB 报告", Dept: "平台", Assembly: "wire_kbreport.go"},
	{Name: "main:tableHealth", Role: "表健康", Dept: "平台", Assembly: "main_monitors.go"},
	{Name: "main:pendingGateWatch", Role: "armed 闸评估环", Dept: "投资", Assembly: "main_monitors.go"},
	{Name: "main:autoMapSweep", Role: "broker 目录扫", Dept: "平台", Assembly: "main_monitors.go"},
	{Name: "main:allowRulesReload", Role: "规则重载", Dept: "平台", Assembly: "wire_whitelist.go"},
	{Name: "setup:httpServer", Role: "HTTP 服务", Dept: "平台", Assembly: "setup_http.go"},
	{Name: "wire_dashboard:stepMeta", Role: "步骤元数据", Dept: "平台", Assembly: "wire_dashboard.go"},
	{Name: "service_snapshots:purge-acked", Role: "快照清理", Dept: "平台", Assembly: "service_snapshots.go", EventDriven: true},
	{Name: "main:cexProxyHealth", Role: "代理健康", Dept: "平台", Assembly: "wire_health.go"},
	{Name: "main:egressProbe", Role: "出口探针", Dept: "平台", Assembly: "wire_health.go"},
}

// wireEmployeeRoster 装配：编制入库 + safego 心跳/退出钩→PG。未注册名心跳=
// 装配漏登或子节拍误走带 Beat 形态——转聚合 warn 告警（24h 去重），不落台账行。
func wireEmployeeRoster(ctx context.Context, st *pgstore.Store, cfg *config.Config, refreshTopic *bus.Topic[string]) {
	// 条件门同步（roster 建模错位修②）：gate 未开=该 daemon 本设计不启——
	// 不注册对应行+存底翻离职（enabled=f 如实注销，非假离线）。BINANCE_
	// PROXY_URL 空=cexProxyHealth fail-open 不启（wire_health.go 同判据）；
	// mt5a adapter cfg nil=mt5-quote-fwd 门内不 spawn（setup.go 同判据）。
	gated := map[string]bool{}
	if cfg.BinanceProxyURL == "" {
		gated["main:cexProxyHealth"] = true
	}
	if loadMT5Config() == nil {
		gated["setup:mt5-quote-fwd"] = true
	}
	for i := range employeeRoster {
		if gated[employeeRoster[i].Name] {
			if err := st.RetireEmployee(ctx, employeeRoster[i].Name); err != nil {
				log.Printf("[roster] retire %s: %v", employeeRoster[i].Name, err)
			}
			continue
		}
		if err := st.RegisterEmployee(ctx, &employeeRoster[i]); err != nil {
			log.Printf("[roster] register %s: %v", employeeRoster[i].Name, err)
		}
	}
	safego.SetOnBeat(func(name string) {
		bctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		found, err := st.EmployeeBeat(bctx, name)
		if err != nil {
			log.Printf("[roster] beat %s: %v", name, err)
			return
		}
		if !found {
			if aerr := st.InsertAlertDedup(bctx, "warn",
				"[roster] 未登记心跳（装配漏登/子节拍误带 Beat）："+name, 24*time.Hour); aerr != nil {
				log.Printf("[roster] unregistered beat alert %s: %v", name, aerr)
			}
			return
		}
		if refreshTopic != nil {
			refreshTopic.Publish(dashboard.RefreshEmployees)
		}
	})
	safego.SetOnExit(func(name string) {
		ectx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := st.EmployeeExit(ectx, name); err != nil {
			log.Printf("[roster] exit %s: %v", name, err)
		}
	})
}
