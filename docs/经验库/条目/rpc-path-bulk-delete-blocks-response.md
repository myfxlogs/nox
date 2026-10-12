---
schema_version: 2
id: rpc-path-bulk-delete-blocks-response
kind: pitfall
risk: medium
status: verified
keywords: [RemoveBroker, 慢, 30s, RPC 超时, 批量删除, DELETE, 异步清理, ticks, facts, 前端重试, 幂等]
scope: [dashboard, pgstore, broker-lifecycle, UX]
summary: 删除/级联类 RPC 内含万行级 DELETE 会卡到客户端超时（~30s）——用户重试靠幂等兜底但体验差；正确性小表同步清+批量观测表异步清（safego+超时+失败 warn 告警+重建闸）
first_seen: 2026-10-09
last_verified: 2026-10-09
skills: []
evidence_refs:
  - type: decision
    ref: D-151
  - type: commit
    ref: 6f5f7b4
  - type: doc
    ref: internal/dashboard/broker_manager_lifecycle.go:148
  - type: doc
    ref: internal/store/pgstore/venue_purge.go:65
  - type: server_fact
    source: postgres
    query: docker logs arb-core | grep "removed <venue>"
    observed_at: 2026-10-09T11:01:00Z
    server_revision: arb-core @849e48764
    response_hash: ""
---

## 事实/症状

Broker 页删除停用账号 ~30s 才返回，前端超时致用户重复提交（生产日志：52993526 连续 "removed" 三次，首次 purged facts:86567+ticks 全量，后续幂等空转）。

## 根因

`RemoveBroker` 同步路径里 `PurgeVenueData` 顺序跑 5 条 DELETE，其中 ticks/facts 各数万行（有 venue 前导索引但批量堆删+WAL 在小 VPS 上就是 10-30s 量级）。正确性并不需要这些观测行在响应前清完。

## 处置/修法

劈两半（D-151）：

- **同步**：open 真金仓闸 → 停适配器 → qbus 驱逐 → 派生小表清（纸面退出/asset_equiv/fin_carry_universe/fin_xasset_monitor）→ 符号映射 → 账号行 → 返回（亚秒）。
- **异步**（safego+10min ctx）：facts 先删——发现层 `BrokerVenueDiscoverable` 对缺席账号 fail-open（`!ok`→可配对），facts 残留可产幽灵 universe 行，故最先清最短窗；ticks 次之；末尾重扫 universe/monitor 收扫描竞态行。
- **失败兜底**：异步清理失败写 `warn` 告警（残留=系统解决不了=推送域）。
- **重建闸**：异步起步查 `broker_accounts`——账号被重建则放弃清理（强清会擦掉新账号刚采集的数据）。

## 预防/检查

- 任何 RPC 路径里新增表清理时估行数：>~1k 行的 venue 级 DELETE 一律挪异步或证明够快。
- 异步清理必须：自带 ctx（RPC ctx 返回即取消）、超时、失败可感知（告警非仅 log）、幂等、重建闸。
- fail-open 谓词（`!ok`→放行）意味着「行删了但数据还在」的窗口内残留数据仍是活的——排序让发现层输入最先清。
