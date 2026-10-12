// employee_roster.go —— D-190：员工花名册装配（编制=此处显式声明，禁自我声明漂移）。
// role/dept/assembly 在册即裁决留档；cadence_sec=0 常驻型以 running 判生死，
// >0 节拍型超 3×cadence 未心跳=滞跳（心跳写经 safego OnBeat/OnExit 钩，
// 挂现有 loop 节拍零新 goroutine）。
package main

import (
	"context"
	"log"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/bus"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// employeeRoster 编制表（装配点名=wire 文件；未登记者 Beat 时自动补册 role=未登记）。
var employeeRoster = []pgstore.Employee{
	{Name: "main:eng", Role: "交易引擎", Dept: "交易", Assembly: "main.go"},
	{Name: "main:sched", Role: "调度", Dept: "运维", Assembly: "main.go"},
	{Name: "main:cexscan", Role: "CEX 扫描", Dept: "数据", Assembly: "wire_cexscan.go"},
	{Name: "main:cex-io", Role: "CEX IO", Dept: "数据", Assembly: "wire_cex_io.go"},
	{Name: "main:income", Role: "收益采集", Dept: "账本", Assembly: "wire_income.go"},
	{Name: "main:okx_income", Role: "OKX 收益采集", Dept: "账本", Assembly: "wire_income.go"},
	{Name: "main:recon", Role: "对账", Dept: "账本", Assembly: "wire_posmon.go"},
	{Name: "main:explorer", Role: "机会探索", Dept: "研究", Assembly: "wire_explorer.go"},
	{Name: "main:oppmatch", Role: "模板匹配", Dept: "研究", Assembly: "wire_oppmatch.go", CadenceSec: 90000},
	{Name: "main:selfwatch", Role: "自监控", Dept: "风控", Assembly: "wire_selfwatch.go"},
	{Name: "main:feishu-push", Role: "飞书推送", Dept: "IR", Assembly: "wire_feishu.go"},
	{Name: "main:macro", Role: "宏观日历", Dept: "数据", Assembly: "wire_macro.go"},
	{Name: "main:trxvote", Role: "TRX 投票", Dept: "数据", Assembly: "wire_trx.go"},
	{Name: "main:ticker", Role: "行情", Dept: "数据", Assembly: "wire_quotes.go"},
	{Name: "main:watchlist-provider", Role: "自选行情", Dept: "数据", Assembly: "wire_dashboard.go"},
	{Name: "main:carry-universe", Role: "套利宇宙", Dept: "研究", Assembly: "wire_scan.go"},
	{Name: "main:funding-done-bridge", Role: "funding 桥", Dept: "数据", Assembly: "wire_quotes.go"},
	{Name: "main:gate-ws", Role: "Gate WS", Dept: "数据", Assembly: "wire_gate.go"},
	{Name: "main:binance-mkt-ws", Role: "币安行情 WS", Dept: "数据", Assembly: "wire_quotes.go"},
	{Name: "main:okx-ws", Role: "OKX WS", Dept: "数据", Assembly: "wire_quotes.go"},
	{Name: "main:acctMon", Role: "账户监控", Dept: "风控", Assembly: "main_monitors.go"},
	{Name: "main:acctFrameSnapshots", Role: "账户快照", Dept: "风控", Assembly: "main_monitors.go"},
	{Name: "main:cexExecMgr", Role: "CEX 执行管理", Dept: "交易", Assembly: "main_monitors.go"},
	{Name: "main:cexPosMgr", Role: "CEX 持仓管理", Dept: "交易", Assembly: "main_monitors.go"},
	{Name: "main:mtHealth", Role: "MT 健康", Dept: "风控", Assembly: "main_monitors.go"},
	{Name: "main:tableHealth", Role: "表健康", Dept: "运维", Assembly: "main_monitors.go"},
	{Name: "main:pendingGateWatch", Role: "闸态监控", Dept: "运维", Assembly: "main_monitors.go"},
	{Name: "main:autoMapSweep", Role: "broker 目录扫", Dept: "运维", Assembly: "main_monitors.go"},
	{Name: "main:idleCapital", Role: "闲置资金", Dept: "研究", Assembly: "main_monitors.go"},
	{Name: "main:probeAdjudicator", Role: "探针裁决", Dept: "研究", Assembly: "main_monitors.go"},
	{Name: "main:bpMon.Run", Role: "BP 监控", Dept: "风控", Assembly: "main_monitors_bpmon.go"},
	{Name: "main:bpMon.orderKick", Role: "BP 补单", Dept: "交易", Assembly: "main_monitors_bpmon.go"},
	{Name: "main:dci-pool-reconcile", Role: "DCI 池对账", Dept: "账本", Assembly: "dci_go.go"},
	{Name: "probe-report", Role: "探针报告", Dept: "研究", Assembly: "wire_dashboard.go"},
	{Name: "main_helpers:disabledStrategiesListener", Role: "淘汰监听", Dept: "交易", Assembly: "main_helpers.go"},
	{Name: "setup:httpServer", Role: "HTTP 服务", Dept: "运维", Assembly: "setup_http.go"},
	{Name: "setup:atEngine.orderKick", Role: "引擎补单", Dept: "交易", Assembly: "setup.go"},
	{Name: "setup:mt5-quote-fwd", Role: "MT5 行情转发", Dept: "数据", Assembly: "setup.go"},
	{Name: "setup:swapReconciler", Role: "swap 对账", Dept: "账本", Assembly: "setup.go"},
	{Name: "setup:swapLedger", Role: "swap 账本", Dept: "账本", Assembly: "setup.go"},
	{Name: "wire_dashboard:stepMeta", Role: "步骤元数据", Dept: "运维", Assembly: "wire_dashboard.go"},
	{Name: "main:cexProxyHealth", Role: "代理健康", Dept: "运维", Assembly: "wire_health.go"},
	{Name: "main:egressProbe", Role: "出口探针", Dept: "运维", Assembly: "wire_health.go"},
}

// wireEmployeeRoster 装配：编制入库 + safego 心跳/退出钩→PG（每 beat 一条 upsert，
// 频率=loop 启动/退出/内节拍，写面极低）。
func wireEmployeeRoster(ctx context.Context, st *pgstore.Store, refreshTopic *bus.Topic[string]) {
	for i := range employeeRoster {
		if err := st.RegisterEmployee(ctx, &employeeRoster[i]); err != nil {
			log.Printf("[roster] register %s: %v", employeeRoster[i].Name, err)
		}
	}
	safego.SetOnBeat(func(name string) {
		bctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := st.EmployeeBeat(bctx, name); err != nil {
			log.Printf("[roster] beat %s: %v", name, err)
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
