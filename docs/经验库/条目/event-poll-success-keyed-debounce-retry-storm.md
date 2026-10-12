---
schema_version: 2
id: event-poll-success-keyed-debounce-retry-storm
kind: pitfall
risk: high
status: verified
keywords: [防抖, debounce, 事件触发, 轮询退化, 重试风暴, lastSuccess, margin poll, OnOrderProfit, 同步阻塞, 超时定速]
scope: [adapter, mt5, collect, event-driven, retry]
summary: 事件触发采集的防抖键绑在「成功态」上——失败路径下一帧立即重试退化为超时定速轮询，且同步 RPC 阻塞同一消费循环饿死其他帧
first_seen: 2026-09-30
last_verified: 2026-09-30
skills: [事实核验, 生产取证]
evidence_refs:
  - type: decision
    ref: D-006 S1b
  - type: commit
    ref: eb1f863e
  - type: doc
    ref: internal/adapter/mt_margin_source.go
  - type: server_fact
    source: postgres
    query: docker logs grep 'event-triggered poll failed'——13:12:03~15 五连发每 3s 一次=swapPerCallTimeout 定速
    observed_at: 2026-09-30T13:12:00Z
---

## 事实/症状

`MarginFactSource.consumeStream` 事件触发 margin 采集：帧到 → `marginPollOnce`（内含
3s 超时 AccountSummary RPC）→ 失败打日志。日志实测 13:12:03/06/09/12/15 五连发，
节拍恰为 `swapPerCallTimeout=3s`——超时值本身在定速，不是事件频率。

## 根因或策略解释

防抖条件 `time.Since(s.lastSuccess) < marginDebounce` 的基准**只在成功时推进**。
失败路径不更新任何时间戳 → 下一帧到达防抖永不命中 → 每帧一次失败 RPC。
设计者假设「事件稀疏=天然短重试」，但 OnOrderProfit 在有仓账户上随浮盈逐 tick
发射——事件不稀疏。双重伤害：poll 同步阻塞 consumeStream，3s 卡死期间同帧流的
publishPositions/publishAcctFrame 排队等——持仓/账户帧被饿死（data_stale 告警饿源）。

## 处置

- **通用律**：事件驱动+失败重试的防抖/节流键必须绑在「尝试态」（lastAttempt）而非
  「成功态」，或失败路径带独立退避——否则失败=解除节流的隐形通道。
- **本例根治**（D-006 S1b）：准静态数据（杠杆）不应帧驱动——降为 Connect/重连成功
  各采一次，消费循环回归纯流零同步 RPC。
- **形态判据**：流消费循环内放同步阻塞 RPC 本身就是形态错误——取数与消费分层。

## 证据

- check: `docker logs | grep 'event-triggered poll failed'`
- result: 19min 窗口 9 次，3s 等距节拍；同窗口两户 data_stale 告警
- ref: D-006 S1b（eb1f863e 删除该路径）；部署后 poll 失败日志归零

## 反证条件

- check: 检查其他事件触发消费源是否有同类「成功态防抖」结构
- falsifies_when: 证明事件源天然稀疏（如日均<10 帧）且消费循环无其他帧职责——则
  成功态防抖无实害（仍建议尝试态，便宜）

## 适用边界

一切「事件帧 → 同步采集」结构；对纯计数/统计类帧中不阻塞主消费循环的采集可放宽。
