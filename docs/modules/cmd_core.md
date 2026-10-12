# cmd/core — 进程装配层（M2.6）

## 职责

单二进制入口。main.go 依序完成：配置/存储/qbus → **员工花名册**（D-190/D-211，须在首个 worker 前装 safego beat/exit 钩）→ quote 流 → 采集调度 → 引擎/持仓监控 → brokerMgr/对账 → 执行管线 → dashboard+HTTP。

## 结构

- `main.go`：装配主干（≤300 行门禁，重装配拆 wire_*.go）
- `main_helpers.go` / `main_monitors*.go`：采集/监控批量装配
- `setup.go` / `setup_http.go` / `setup_readyz.go`：autotrade/swapledger/HTTP 起停+readiness
- `employee_roster.go`：编制表+心跳钩→pgstore（beat 成功 → `RefreshEmployees` topic 推流，D-230①）
- `wire_*.go` ×30：各域装配点（cex_io/cex_paper/cexscan/dashboard/dephealth/depth/explorer/feishu/gate/graduation/health/income/kbreport/macro/oppmatch/posmon/quotes/recon/scan/selfwatch/servertz/sleepmon/trx/trxvote/wheel/whitelist 等）
- `wire_*_test.go` ×7：装配测试随件
- `web/embed.go`：`//go:embed web/dist`（NOX_WEB_DIR 非空=dev 目录服务）

## 不搬域（清单 §11 Q7）

cmc / intra / pricealert 三装配族不入 M2.6——对应 wire 文件、roster 行、CMC 健康注入、日内扫描、价格提醒引擎全部剔除。

## 员工口径（D-220/D-225⑤）

Dept 十一枚举：数据/研究/投资/交易/风控/运营/账本/复盘/IR/运维/合规；cadence=0 常驻按 running，>0 超 3× 未 beat=滞跳；`ListEmployees` 全员（roster+Beat 自补册）。
