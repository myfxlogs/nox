---
schema_version: 2
id: race-green-serial-tests-blind
kind: pitfall
risk: high
status: verified
keywords: [race 全绿, 数据竞争, 串行单测, mutex, 每源一 goroutine, TriggerNow, 跨源回调, 架构判据]
scope: [并发代码复审, Collector 架构, 回调注入]
summary: -race 绿只证明「测试跑过的并发路径」无竞争——单测全部串行调用时检测器全盲；判据应该是架构事实（谁在哪个 goroutine 触达该状态），不是测试结果。每源一 goroutine 的调度器里，跨源回调（A 源 onChange 调 B 源方法）必然构成双 goroutine 触达，共享状态必须持锁。
first_seen: 2026-09-05
last_verified: 2026-09-05
skills: [事实核验]
evidence_refs:
  - type: commit
    ref: f2e4339
  - type: doc
    ref: internal/collect/sched.go:22
---

## 事实/症状

D-231 S5：AprCollector 被调度器 trx_apr goroutine（5min Poll）与 trx_account goroutine（onChange→TriggerNow→Poll）双 goroutine 触达，`lastTrigger`/聚合值字段裸读写。施工方与复审方首轮均报「race 全绿」——但全部单测对 AprCollector 都是**串行**调用，`-race` 检测器没有并发路径可观测，绿是无信息的。复审读 sched.go（「每源一个 goroutine」写在注释 L22）才发现竞争是构造性存在。

## 根因

①把「race 检测通过」误当「线程安全」证明——检测器只覆盖测试实际并发的路径；②复审未先读调度并发模型就采信机检绿；③跨源回调注入（A 源 onChange 调 B 源方法）天然把 B 的状态暴露给第二个 goroutine，设计时无人画「谁在哪个 goroutine」这张表。

## 修法

并发代码复审固定第一问：**该类型的每个方法分别被哪个 goroutine 调用？**（读调度器/接线，不读测试）；共享状态判竞争靠架构图，判安全才靠 -race；修复必须有**并发压测用例**（多 goroutine 同时打该方法，-race 下跑）作回归门，串行用例对锁的正确性零证明力。

## 反证条件 / 适用边界

单 goroutine 消费的类型（如仅自身 ticker 触达）无需锁；immutable/构造后只读状态除外。若调度器未来改单循环串行，锁可撤——架构变则判据变。

## 泛化判断

可晋升 ~/kb（与 [[session-token-read-write-race]] 互补：那条是「定向 race 绿、全量并发才炸」的同一测试盲区家族，本条给出复审方法论=先问 goroutine 归属再看测试结果）。随下次收工晋升。
