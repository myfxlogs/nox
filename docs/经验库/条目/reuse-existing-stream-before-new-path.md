---
schema_version: 2
id: reuse-existing-stream-before-new-path
kind: pitfall
risk: high
status: verified
keywords: [复用优先, 数据源替换, streamUpdates, Topic 扇出, 冗余数据通路, 新造轮询, SSE, 实时推送]
scope: [前后端数据通路, 监控卡片, 实时数据]
summary: 需求本质=「把某数据源换成另一个」（如对冲卡报价币安→CMC）时，正确改动=换源接入既有管线（Topic 扇出/streamUpdates/既有 RPC）；实际却另起炉灶造平行通路（独立 RPC+独立轮询+独立状态），冗余层层叠加。防法=动数据通路前先 grep 既有 Topic/stream/管道，「换源≠造路」。
first_seen: 2026-09-04
last_verified: 2026-09-04
skills: [KB维护, 事实核验]
evidence_refs:
  - type: commit
    ref: 08619cc
  - type: doc
    ref: web/src/hooks/useSnapshot.ts
  - type: decision
    ref: D-049
---

## 事实/症状

对冲结构卡：D-212 造独立 RPC → 数据源换 CMC（D-208）后卡不动 → R8 加 30s 独立轮询（用轮询修轮询）→ 业主点破「只是把数据源从币安换成 CMC 而已」。而前端早有 streamUpdates 实时流（D-049 个人持仓现价/D-090 报价 1s 蓄压/D-052 case 8 全量广播），流健康时零轮询——对冲卡是全页唯一没坐快车道的组件，且被叠加了第二重轮询。

## 根因

把「换数据源」误做成「造新通路」：未先盘点既有管线（useSnapshot 流循环/Topic 扇出先例），从卡片局部出发逐层打补丁，每层补丁都成为下一层的地基。

## 修法

R10：WsClient 事件→hedgeTopic（D-049 模式）→1s 蓄压→streamUpdates case 12→前端全量替换，删 R8 轮询。**规则**：动任何数据通路前，先 grep `Topic|stream|SSE|Subscribe|setInterval` 盘点既有管线；「换源≠造路」写进自审 A 项（复用优先）的检查动作。

## 反证条件 / 适用边界

确无既有管线时可新造（但须 D# 记录为何不复用）；既有管线语义不匹配（如需要不同 QoS/隔离域）时例外需显式论证。

## 泛化判断

可晋升 ~/kb（LLM 补丁式思维的通病：局部修补替代架构对齐）。随下次收工晋升。
