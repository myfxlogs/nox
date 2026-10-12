---
schema_version: 2
id: silent-venue-equity-staleness
kind: pitfall
risk: high
status: verified
keywords: [权益快照陈旧, idle_funds, AccountMonitor, EquityObserved, gate, 私流静默, 事件化, asOf, D-571, D-177]
scope: [internal/dashboard, AccountMonitor, 机审 premise 回放, CEX 私流, gate]
summary: 事件化 AccountMonitor 下私流静默 venue（gate 闲置零 balances 帧）权益 asOf 只随 boot/kick 刷新——5min 阈后机审 idle_funds 对该 venue 一律「权益快照陈旧」硬伤拒，私流活着但权益证据链结构性断流
first_seen: 2026-10-11
last_verified: 2026-10-11
skills: [生产取证, 事实核验]
evidence_refs:
  - type: decision
    ref: D-177
  - type: doc
    ref: internal/dashboard/account_monitor.go:140
  - type: doc
    ref: internal/dashboard/service_probe.go:35
  - type: server_fact
    source: postgres
    query: SELECT channel,count,max(received_at) FROM cex_private_events WHERE venue='gate' GROUP BY channel; fin_venue_account_snapshot 同源
    observed_at: 2026-10-11T01:00:00Z
    server_revision: SG pg:15433
    response_hash: gate 事件 49 条止于 10-08 14:09（末 4 条=futures.system 升级广播）；gate:perp 快照最后行 09-30
---

## 事实/症状

- 机审 `idle_funds` premise 对 gate venue 一律「权益快照陈旧」硬伤拒（`probeEquitySnapshotMaxAge`=5min）。
- `cex_private_events` gate 共 49 条，止于 2026-10-08 14:09 UTC；末尾 4 条是 `futures.system`「service upgrade please reconnect」广播——不是业务帧断线证据。
- `fin_venue_account_snapshot` gate:perp 最后行 09-30——钦定（scheduler 面 3d-P1#1c 退役，留痕只走值变搭车写口；gate 非 REST-only venue 故无 rest_poll 行）。

## 根因或策略解释

**私流没死**：docker logs 实证 `subscribed futures.balances/positions/orders/…` 全频道订阅成功、D-166 30min 周期重订阅探针在跑；gate 闲置（无持仓无余额变动）→ 业务频道零帧=事件驱动正常静默。10-08→10-10 窗口是否真死过不可证（容器 10-10 23:44 重启、旧日志清）——但当前订阅健康，「断帧」证据链不成立。

**真病灶**：D-571 事件化删净周期 ticker 后，`EquityObserved.asOf` 只在三处刷新——boot 全量 poll / `Kick(vk)` 定向 REST 回读 / 私流 balances 帧（`balLedger.Put→OnLedgerAcct`）。gate 三源皆静：私流零帧→零 kick；`Kick` 装配源=margin 事件/okx 推送/cexStreamMgr.AcctKick，gate 无一命中 → asOf 冻结于上次 boot poll → 5min 后陈旧 → **该 venue 对机审永久半盲**（直到下次重启/Refresh/kick）。

「按需 Kick 回读」径（`pollVenueKeys`）钦定存在但机审未接——修向=asOf 陈旧时触发定向回读复判而非直拒。

## 处置

核查定性（D-177）：**真 bug 候选→转修复票**（机审 stale 分支接按需 Kick 复判 / 静默 venue asOf 续命径，由复审定案）。本票零语义变更。

## 证据

- check: SG pg `cex_private_events` gate 分组计数+`futures.system` payload 原文
- result: 49 条、末 4 条 venue 升级广播；私流 docker logs 全频道 subscribed
- check: `account_monitor.go` Run/Kick/pollVenueKeys + `wire_gate.go`/`main_monitors.go` kick 装配源
- result: gate 无任何 kick 装配源；asOf 无周期续命
- check: `fin_venue_account_snapshot` gate:perp 末行 09-30 + `rest_poll` 行只有 okx funding/earn
- result: 停写=scheduler 退役钦定+gate 非 REST-only 不落该面=正常
- ref: D-177（LOG 取证节）；**已修 D-178**——机审权益闸 stale/缺分支接 pollVenueKeys 按需回读复判（两拍一次复判），本指纹死锁已解

## 反证条件

- check: `Kick("gate")` 后 `EquityObserved("gate:perp")` asOf 应刷新；若 REST 回读失败则另有凭据/连通病灶
- falsifies_when: gate 出现 kick 源（如有持仓触发 balances 帧）仍陈旧，或 REST 回读返回错误 → 病灶升级为「回读失败」非「无 kick」

## 适用边界

适用所有「私流在但业务静默」的 venue（gate 闲置期、binance funding 等无私流频道类型）。MT 侧有 margin 事件 kick 源不受影响。
