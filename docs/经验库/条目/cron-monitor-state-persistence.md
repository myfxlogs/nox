---
schema_version: 2
id: cron-monitor-state-persistence
kind: pitfall
risk: high
status: observed
keywords: [cron, 监控, 告警, 状态机, 跨进程, 持久化, Go, PostgreSQL]
scope: [探针, dashboard, monitoring, alerts, PostgreSQL]
summary: 一次性 cron 监控不能依赖进程内去重和恢复状态；跨进程告警必须持久化 run/state，否则重启后重复告警、恢复丢失或状态假绿
first_seen: 2026-09-05
last_verified: 2026-09-05
skills: [KB维护, 验证与交接]
evidence_refs:
  - type: decision
    ref: D-227
  - type: doc
    ref: docs/CN-FI设计独立审计与最优解意见-2026-09-05-Devin.md:59-74
  - type: doc
    ref: internal/store/pgstore/opportunities.go:116-137
  - type: doc
    ref: internal/dashboard/mt_health_monitor.go:62-67
---

## 事实/症状

独立探针由 host cron 周期启动，每次运行直接调用告警写入接口；现有告警表没有信号键、resolved 状态或观测值，现有监控器的去重和重告警时间保存在进程内存。探针重启后会重复告警，无法可靠产生恢复事件；直接写数据库也不会触发另一个 core 进程内的实时 topic。

## 根因或策略解释

执行边界从长生命周期进程变成一次性进程后，内存状态不再是状态真相源。只复用“写 alerts 的函数”不等于复用完整监控语义；告警生命周期、探针运行结果和最新观测必须跨进程持久化。

## 处置

保留探针一次运行模型时，增加脱敏的 probe run 记录和按信号键的持久 state；按状态转换或有界重告警写入既有 alerts。前端明确使用持久快照/刷新语义，不能暗示跨进程 WS 实时推送。若不需要生命周期告警，则应删除告警需求，只保留可查询事实状态，不能用无状态 InsertAlert 冒充监控闭环。

## 证据

- check: 读取 `internal/store/pgstore/opportunities.go` 的 `InsertAlert` 与 `alerts` 查询实现
- result: 写入接口只负责 INSERT；表字段只有 id/rule_id/ts/level/message/acked，未提供跨进程状态存储
- ref: `internal/store/pgstore/opportunities.go:105-137`；D-227
- check: 读取 `internal/dashboard/mt_health_monitor.go` 的状态字段与告警逻辑
- result: 去重状态和最后 critical 告警时间位于进程内 map；一次性 cron 不能跨运行复用
- ref: `internal/dashboard/mt_health_monitor.go:62-67,257-296`

## 反证条件

- check: 让探针重启、重复执行同一 as_of，并验证状态转换、恢复告警和 core 重启后的结果
- falsifies_when: 在没有额外持久化 state 的前提下，重启后仍能证明告警幂等、恢复可重放且跨进程可见；否则本条继续成立

## 适用边界

适用于一次性 cron、批处理探针、外部 worker 直接写入共享告警表的监控系统。长生命周期单进程监控且状态已持久化时，不应机械新增同类表；但仍须验证重启和跨进程可见性。
