---
schema_version: 2
id: monitor-scope-from-persistent-residue
kind: pitfall
risk: medium
status: verified
keywords: [监控检查集, 持久存储残留, 幽灵venue, 假告警, mt-health, facts残留, querier注册表残留, 同型复发, 数据级处置]
scope: [dashboard, mt-health, 监控告警系统]
summary: 监控系统的检查集（被监控对象集合）从持久存储残留（facts/快照/无 TTL 表）构建而非「预期活跃」信号（运行中适配器/账号 status）构建 → 停用/删除实体永久残留被监控 → 永久假 critical；同型三次复发证明数据级处置不解决类问题，必须代码级收紧检查集来源。
first_seen: 2026-08-29
last_verified: 2026-09-23
skills: [KB维护, 事实核验]
evidence_refs:
  - type: decision
    ref: D-203
  - type: decision
    ref: D-216
  - type: decision
    ref: D-400
  - type: commit
    ref: d36cdd2
  - type: commit
    ref: 61652461
  - type: commit
    ref: 3b7a10d7
  - type: doc
    ref: internal/dashboard/mt_health_monitor.go:122
---

## 事实/症状

mt-health 监控把 `EXR59892915`（业主停用账号 status=stopped）当活 venue 检查：日志 `q:n/a/s:-/m:-` 持续出现，alerts 每 30min 一条 `quote critical (age=n/a)`（R-c3 稳态重发）。同型第一例=538273（8-29，已删账户 facts 残留 93 行 → 永久 critical，D-203 C 处置=DELETE facts）。9-4 第二例复发（59892915 停用未删，facts 残留）。

关键日志形态：`n/a ≠ 故障 当且仅当 stopped`（LOG.md 6109 运维误判教训——曾把停用 venue 的 n/a 当重连骰子误判）。

第三径（D-400，2026-09-23）：`AddStaticQuerier` 注册的 env 适配器 querier 不随 DisableBroker 注销（broker_manager 只清 running/reg）——venueSet 径①无条件收录 stopped 账户（ic52993526/ICR15032313 自 08-20/08-29 停用仍 quote critical ×19/×11）。修法同源：检查集消费侧收紧——venueSet 成型后统一按 `status=="stopped"` 排除（failed 不排），statuses err=不排除 fail-open（3b7a10d7；部署+生产取证=alerts 零新增 critical，复审收编终态）。

## 根因或策略解释

检查集来源错误：`venueSet = qbus ∪ facts ∪ queriers` 并集中，facts 表无 TTL——停用/删除账号的 venue 行永久残留 → 收录进检查集 → `collectQuoteAges` 查不到（age=0）→ `classifyAge` 零值=metricCrit → 永久 critical。qbus.AllLatest 保留死 venue 最后报价（D-205 型掉线检测价值，不能动）→ 热停用场景第二条幽灵路径。

数据级处置（DELETE 残留行）只解决实例：下一个停用的账号（业主测试 broker 是常态操作）复发同型。类问题必须代码级：检查集 = 预期活跃信号（运行中 queriers ∪ status=connected ∪ 未知 qbus venue），持久残留降级为查询表不再是集合来源。

## 处置

D-216（9-4）：`venueSet = mtSet(queriers) ∪ connectedSet(broker_accounts.status='connected') ∪ (qbusVenues \ knownSet)`。停用账号三路全不进（facts+qbus 两条幽灵路径都堵死）；connected 掉线照告（保 D-205 检测价值）；CEX（不在 broker_accounts）经 qbus\known 继续监控；不删数据（残留 facts 惰性化，Enable 恢复 status=connected 即恢复监控）。

## 证据

- check: `docker logs arb-core | grep 59892915` → 全容器生命周期仅 1 条非 mt-health 日志（零连接尝试=适配器未启动）；`grep -E "ready" | grep 59892915` 零命中
- result: StartAll 只启 status=connected 账号；59892915 不在启动列表，venueSet 经 DB 残留 facts 收录 → 永久 q:n/a critical
- ref: D-203 C（538273 先例+DELETE 处置）、D-216（第二例+代码根除）、LOG.md:6109（运维误判教训）
- ref: 第三例 2026-09-22 ic52993526——业主以为已停用但 broker_accounts.status 仍=connected（停用意图未走 SetBrokerDisabled RPC），mt-health venueSet 经 connectedSet 收录持续 critical age=n/a；处置=SetBrokerDisabled(venue,disabled=true) → status=stopped → venueSet 自动剔除。**变体复发面**：autotrade SWAPARB 发现引擎 venue 集仍从 broker_symbol_map 残留构建（不过滤 status）→停用 venue 继续参与配对评估刷 product group mismatch 噪音日志（不交易但扰动），同根不同系统。
## 反证条件

- check: 部署 D-216 后观察 mt-health 日志与 alerts 表
- falsifies_when: EXR59892915 行消失但 connected venue 网关掉线（D-205 型场景）也不再告警 → 修过头（杀掉真检测价值）；或 CEX venue quote 监控静默丢失 → qbus\known 路径推导错误

## 适用边界

- 泛化形态：任何「监控/告警/对账系统」的检查集若从持久存储残留构建（无 TTL 表、快照缓存、内存最后值）而非预期活跃信号构建，停用/删除实体都会成幽灵。修复方向=集合来源换成预期活跃信号；「保留死实体最后数据」若另有检测价值（如掉线检测）须保留数据但收紧集合成员资格。
- 本例具体保真约束（勿照抄丢）：connected+掉线必须继续告警；不在账号表的 qbus venue（CEX）必须继续监控。
