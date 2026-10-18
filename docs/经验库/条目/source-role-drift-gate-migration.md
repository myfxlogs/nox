---
schema_version: 2
id: source-role-drift-gate-migration
kind: pitfall
risk: medium
status: observed
keywords: [源语义漂移, 降频闸, 兜底件, 主源角色变迁, gate migration, stale 误报, wsHealthy, REST 主源, 契约层 vs 消费层]
scope: [任何「降级/兜底/节流」消费侧闸件, 数据源角色变迁（兜底↔主源）的代码改动, collector/poller 降频设计]
summary: 数据源角色在契约层变迁（兜底↔主源）而消费侧降频/兜底闸不随迁=闸的语义从「兜底节能」漂成「主源限速」——节拍被压贴告警阈=周期误报。实证：D-225 时代 CMC REST=WS 兜底挂 wsHealthy 15min 降频闸，D-212 裁 REST 转 instrument 价主源（WS 无 instrument 订阅位）后闸未随迁→节拍~4m50s 恰贴 staleThreshold=5min 周期误报。修法=改源角色的票必须带「消费侧闸随迁核查」子项——全仓 grep 该源的降级/节流/兜底件逐件过闸后语义复审。
first_seen: 2026-10-19
last_verified: 2026-10-19
skills: []
evidence_refs:
  - type: file
    ref: internal/data/collect/cmc/collector.go（wsHealthy 15min 降频闸摘除点——f2f90812b 修）
  - type: file
    ref: cmd/core/wire_cmc.go（prices.SetWSHealthy 注入摘除——注释改 D-212 主源语义）
  - type: doc
    ref: docs/handoff/STATE.md（cmc-stale 票行——实证段「节拍~5min 贴 5min 阈周期误报」）
  - type: commit
    ref: f2f90812b（修件——闸四点摘除，cmc 51 测绿）
  - type: sg
    ref: SG stale 告警实拍 10:24:13「5m26s src=cmc_rest」+部署后 rest 行 ~65s 全速连落（10:37:59/10:39:04/10:40:09）
---

## 事实/症状

D-225 v2.1 时代设计：WS 为 instrument 价主径、REST 为兜底——`PricesCollector` 挂 `wsHealthy && 15min 内短路` 降频闸（节能目的=WS 健康期 REST 低频顶）。D-212 语义修正后：协议面取证实锤 WS 无 instrument 订阅位（subscribe type 表仅 5/6/7 账户域），REST 升格为 instrument 价**唯一主源**——但降频闸未随迁：主源被自身节能闸限速到 ~4m50s-5min 节拍，恰贴 watchdog `staleThreshold=5min`，jitter 即周期误报「5m26s src=cmc_rest」。另叠加效应：WS session 周期性 expired→IsHealthy 假窗让闸偶放穿=节拍更不规则。

## 判定/复现

- 形态签名：「数据行节拍 ≈ 某阈值的整数倍邻域+周期误报」——先查该源消费侧是否有按「角色假设」设的节流/短路闸。
- 全仓 grep 该源的降级/节流/兜底件：`SetWSHealthy|fallback|throttle|rate.*limit|interval`——逐件核「闸建立时的源角色 vs 现源角色」是否一致。
- 根因二分：闸码所在层=消费层（collector/wire 装配），源角色变更记录在契约层（D-票/协议取证）——两层不同步=本坑。

## 规避/修复

- **改源角色的票必带「消费侧闸随迁核查」子项**：源角色变更（兜底↔主源/轮询↔流）落地前先全仓 grep 该源的消费侧闸件，逐件复审闸后语义。
- **闸件语义注释随件**：降频/兜底闸注释写明「本闸成立前提=源角色 X」——角色变=注释漂移即报警点。
- **阈值与节拍解耦**：watchdog 阈值按「真断流探测」定标，禁按「现节拍+jitter」凑——节拍由闸定而非由 venue 定，阈贴节拍=误报自造。
