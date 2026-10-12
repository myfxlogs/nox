---
schema_version: 2
id: nullable-column-nonpointer-scan-latent-bomb
kind: pitfall
risk: high
status: verified
keywords: [NULL, cannot scan NULL, pgx, 非指针, 可空列, COALESCE, expected_net_bps, ListPositions 500, latent, 语义变更激活, 监控致盲]
scope: [pgstore, settlement, autotrade, dashboard]
summary: 可空列被非指针 dest 直扫是 latent 炸弹——写方把 NULL 合法化瞬间引爆全部读方（ListPositions 500 致盲 2.5h）；改列语义前必须全仓核查读方扫描容错 + NULL 行防回归测试
first_seen: 2026-08-23
last_verified: 2026-09-17
skills: [KB维护]
evidence_refs:
  - type: decision
    ref: D-087 返修③复审/返修④复审
  - type: commit
    ref: 0e50193
  - type: commit
    ref: 239cf34
  - type: doc
    ref: internal/store/pgstore/positions.go:154
  - type: server_fact
    source: rpc
    query: DashboardService/ListPositions（curl 复现）
    observed_at: 2026-08-23T13:45:00Z
    server_revision: 返修②镜像（15:03 构建）
    response_hash: ""
  - type: commit
    ref: 791692b6
  - type: doc
    ref: internal/store/pgstore/table_health.go:31
---

## 事实/症状

- 生产 `arb.dashboard.v1.DashboardService/ListPositions` 500，错误体 `can't scan into dest[4] (col: expected_net_bps): cannot scan NULL into *float64`（curl 复现）。
- 同错误每 ~10s 刷屏：monitor.go:46 / main.go:215 / engine.go:154 三循环（position 监控+autotrade 持仓扫描+review 闭仓列表）自容器启动秒起致盲，2.5h 内 1011 次。
- 位置：`Position.ExpectedNetBps` 为非指针 `float64`，4 条 SELECT 直扫 `expected_net_bps`；positions 表 5 行 NULL（#264~268 SWAPARB，D-087 挪列后的合法空态）。

## 根因或策略解释

- 扫描脆弱性 **pre-D-087 既有**（latent）：非指针 dest 对可空列从一开始就是错的，只是历史上恰好没有 NULL 行而未爆。
- D-087 把 SWAPARB 的 expected_net_bps 挪列、NULL 成为合法写入值 → 读方瞬间引爆。**设计只改了写方语义，没核查读方扫描容错**（D-084 同型：改一头）。
- 单测只测函数自身/正常行 → race 全绿漏网；NULL 行防回归测试缺位。

## 处置

- 代码修法（最小）：4 条 SELECT `expected_net_bps` → `COALESCE(expected_net_bps, 0)`（写入路径不动，NULL 仍合法）；备选 `*float64` 字段 blast radius 更大不取。
- 紧急止血（数据侧，业主授权）：备份 5 行 → `UPDATE ... SET expected_net_bps=0 WHERE IS NULL` → 500→200、监控复明；置 0 安全性预验：SWAPARB 退出路径走 facts 重算不读该列+收敛路径 IsCarryType 类型门隔离。
- 防回归：裸 SQL 直造真 NULL 行（绕开写 0 的测试 helper）× 全部读方法断言不炸。

## 证据

- check: `curl ListPositions` 复现 500 → UPDATE 置 0 后 200（50 持仓）；`docker logs` NULL 错误 1011 次起于容器启动秒
- check: `git show 6c6e78e~1:positions.go` 证明 pre-D-087 已是非指针扫描（latent 定性）
- check: TestListPositions_NullExpectedNetBps（DSN-gated）PASS 非 SKIP；全量 race exit=0
- ref: D-087 返修③复审（decisions.md）/ 0e50193 / 239cf34 / c42f292（部署+基线重建）
- check: D-301a `TableMaxTs` 空表 `max()` 返回 NULL → `var ts time.Time` + `Scan(&ts)` 报 `cannot scan NULL into *time.Time`；修复改用 `pgtype.Timestamp` 接收，`!Valid` 返回零值
- ref: docs/D-301a复审-退回-空表NULL处理-2026-09-17-Devin.md @e12ac519 / commit 791692b6

## 反证条件

- check: 生产再插一行 expected_net_bps=NULL 后调 ListPositions
- falsifies_when: 若 4 处 COALESCE 后仍报 scan NULL 错（说明还有未覆盖读点）——当前全仓 grep 已证 4 处是全部非指针读点（run_full.go:18 本就 *float64 安全）

## 适用边界

- 结论限于 pgx 扫描模式（SQL 直扫进结构体字段）；ORM/指针 dest 不适用。
- COALESCE 语义=「无值当 0」，仅对该列消费方按类型分派、不把 0 当有效信号时安全（收敛型永远写真值、SWAPARB 被类型门隔离=本案安全前提）。
- 一般化教训：**任何列写入语义变更（尤其 NULL 合法化）前，必须全仓 grep 该列全部读点并核查扫描容错**——latent 缺陷不在变更里，在存量里被激活。
