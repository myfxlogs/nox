---
schema_version: 2
id: loop-initial-block-no-timeout
kind: pitfall
risk: high
status: verified
keywords: [StartLoop, 首轮阻塞, 无超时, 对账停摆, ticker 瘫痪, goroutine 卡死, 就绪竞态, 依赖未就绪]
scope: [exec, ops, workflow]
summary: 长轮询 loop initial 同步执行 + 外部调用无 per-call 超时 = 单点阻塞瘫痪子系统（goroutine 卡死 ticker 永不触发）；同型变体=首轮早于依赖就绪产生误报
first_seen: 2026-08-21
last_verified: 2026-08-21
skills: []
evidence_refs:
  - type: doc
    ref: internal/reconcile/reconcile.go:459 StartLoop
  - type: decision
    ref: 对账修复②缺陷1/2
---

## 事实/症状

对账 StartLoop（5min ticker + initial 立即执行一次）部署后 25 分钟仅首轮 2 条日志后**完全静默**——initial `Run(ctx)` 同步阻塞在某个 venue 网络调用（离线 venue）上，goroutine 卡死，后续 ticker 轮次永不执行=对账停摆。伴随变体：首轮执行比 MT5 adapter 连接完成早 1 秒，查证器集合空 → 误报 critical「无查证器，人工处置」。

## 根因或策略解释

两个反模式叠加：① loop 的 initial 与周期任务共用同步路径且外部调用（RPC/查询）无 per-call 超时——单次挂起即永久挂起；② loop 启动时机早于依赖（adapter/连接池）就绪，首轮以空依赖运行产生误报。与 ~/kb `polling-source-before-dependency-ready`（轮询早于依赖就绪·静默空睡）同族，该条是「误报+瘫痪」变体。

## 处置

铁律：① loop 内每个外部调用包 `context.WithTimeout(ctx, N秒)`，超时 fail-closed 跳过该依赖+计数日志，不炸整轮；② initial 延迟启动（等依赖就绪窗口，如 60s）或依赖未就绪时降级为「下轮复查」不告警不计数；③ loop 部署后验收必查「≥2 轮运行痕迹」。

## 证据

- check: `docker logs` 部署后 25min grep reconcile 轮次；adapter connected 时间戳 vs 首轮对账时间戳
- result: 10:32:00 首轮后零轮次；首个 adapter connected=10:32:01（晚 1s）
- ref: 对账修复②施工提示词 缺陷1/2

## 反证条件

- check: 后续新增 loop 是否全部带 per-call 超时
- falsifies_when: 框架层统一提供带超时的 loop 原语后 → 条目收窄为该原语的使用要求

## 适用边界

所有长轮询/定时任务循环（对账/监控/采集）。一次性 CLI 不适用。
