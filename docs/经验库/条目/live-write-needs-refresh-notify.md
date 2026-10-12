---
schema_version: 2
id: live-write-needs-refresh-notify
kind: pitfall
risk: medium
status: verified
keywords: [前端不刷新, 死数, 余额列, dataRefresh, StreamUpdates, BalanceLedger, 写入无通知, funds, 实时推送, 写活不等于显活]
scope: [dashboard, StreamUpdates 消费面, 全 venue 账户]
summary: 后端活态写入（私域WS帧/流帧/轮询）落地内存后若无显式刷新通知，前端只能等无关写事件或手动刷新——「写活≠显活」；修法=写源侧统一通知口+尾沿合批→dataRefresh topic→页面静默重取
first_seen: 2026-09-29
last_verified: 2026-09-29
skills: [事实核验, 验证与交接]
evidence_refs:
  - type: decision
    ref: D-568
  - type: doc
    ref: docs/施工-D-568-账户资金公共刷新项与开关上限面板Tab化-施工提示词-2026-09-29-Devin.md
  - type: commit
    ref: 9334202e
---

## 事实/症状

「账户开关与上限」页可用资金列体感死数：Binance/OKX/Gate 私域 WS 帧实时写 `bus.BalanceLedger`、MT5 `OnOrderUpdate` 余额帧写 `streamAccts`、monitor poll 变更广播 `AccountSnapshot`——四路写入全部活着，但页面只订阅 `allow_rules` topic，余额要拖到下一次权限写事件或手动刷新才动。业主原话「而不是使用那该死的快照」——数据链其实早已流式（快照只是带徽标兜底），真缺口是写完后的刷新通知。

## 根因或策略解释

写源与消费面之间缺「变更桥」：内存态更新不产生任何 StreamUpdates 事件，前端无由感知。「实时链路」的完整性要查到最后一米——数据到达后端内存 ≠ 到达用户屏幕。判型法：先核实读源是否流式（本例四路皆流），再查写后是否有通知发布——两段分开定性，避免把「缺通知」误诊为「快照源」。

## 处置

- 写源侧统一通知口（D-568：`Service.NotifyFunds` 三源汇入——`BalanceLedger.Put` 钩子 / `ObserveStreamAccount` / `poll` changed）→ 复用 dataRefresh topic（StreamUpdates case 14）发域 topic → 页面订阅后静默整表重取。
- 高频写必须合批：尾沿节流（2s：首即发 + pending 单槽 + AfterFunc 冲刷）——裸逐帧发布会把每个余额事件变成一次前端全量 RPC。
- 回调注入在锁外执行（`Put` 解锁后调 `onPut`），防持锁回调重入死锁。
- 审计同类点：凡「内存态主源+前端展示」链路，验收时追问「写完谁通知谁」，不要只验到后端字段正确为止。

## 证据

- check: `BalanceLedger.Put`（balance_ledger.go:37）/ `ObserveStreamAccount`（account_stream.go:16）/ `poll` changed 块（account_monitor.go:269）逐点核查零 `PublishDataRefresh`；`BrokersPageAllowRules.tsx:42` 只订 `allow_rules`
- result: 四路写入活跃而页面无 funds 类订阅——通知断点实证
- ref: D-568 SSOT §1 病灶实证表（@9334202e）

## 反证条件

- check: 页面已订阅全量 accounts 推流或写源已发 dataRefresh
- falsifies_when: 写后即推已存在且前端订阅正确——此时页面不跳应改查 StreamUpdates 断连/订阅漏 topic

## 适用边界

StreamUpdates 推+拉同源架构（写源 Publish→case 14→前端按 topic 整表重取）的项目；对「前端直连后端状态、无推送通道」的架构，同等问题是缺轮询/订阅机制而非缺通知事件。
