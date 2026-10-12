---
schema_version: 2
id: unready-dependency-alerts-untrustworthy
kind: pitfall
risk: high
status: verified
keywords: [假阳性, 对账告警, adapter 未连接, 依赖未就绪, 持仓存亡, RPC 实证, 裸露误判, 告警不可信]
scope: [exec, ops, workflow]
summary: 依赖未就绪期（adapter 连接前）的对账告警全假阳性（外部空→本地全「未找到」）——存亡判断必须 venue RPC 实证
first_seen: 2026-08-21
last_verified: 2026-08-21
skills: []
evidence_refs:
  - type: decision
    ref: 修复②复审事实更正（LOG 2026-08-21）
  - type: doc
    ref: docs/经验库/条目/loop-initial-block-no-timeout.md
---

## 事实/症状

对账在 MT5 adapter 连接完成前 1~45 秒跑：venue 订单列表为空 → 本地腿全部「在 venue 侧未找到」→ 连环假阳性 critical「可能已平仓」。agent 据此判断 #264「卖腿已被平、真实裸露」并通报业主——RPC 直查证实**双腿健在**，此前的裸露结论完全是假阳性误传。

## 根因或策略解释

「未找到」≠「不存在」——未找到可能是查询侧瞎（依赖未连接）。告警也是某种"文档"：**以事实出发原则对告警同样适用**（server-facts-over-docs 的变体：告警在依赖未就绪期不构成事实）。且误判方向偏危险（虚报裸露→诱发不必要平仓）。

## 处置

① 持仓/订单存亡结论只认 venue 实时查询（RPC/OpenedOrders 直证）；② 见到成批「未找到」先查依赖就绪时间线（告警时刻 vs adapter connected 时刻）再下结论；③ 系统侧修复=未就绪降级不告警（见 [[loop-initial-block-no-timeout]]）。

## 证据

- check: `docker logs` 告警时刻 10:32:00 vs 首个 adapter connected 10:32:01；ListBrokerPositions RPC 实查双腿+posId=264
- result: 假阳性链完全解释；264 为健康对冲持仓
- ref: 修复②复审 LOG

## 反证条件

- check: 依赖就绪后的告警可靠性
- falsifies_when: 系统全面接入就绪门禁（未就绪零告警）后 → 条目收窄为「批量未找到先查时间线」

## 适用边界

所有依赖外部连接的比对/对账/健康检查告警。同依赖正常就绪后的个别未找到不适用（可能是真失联）。
