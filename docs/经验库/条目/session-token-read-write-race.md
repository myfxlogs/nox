---
schema_version: 2
id: session-token-read-write-race
kind: pitfall
risk: high
status: verified
keywords: [token, 数据竞争, data race, 会话重连, reconnectSession, withSessionMD, CI race, 时序相关, 无锁读]
scope: [adapter, mt5, mt4, test]
summary: 会话 token 写（重连）持锁、读（withSessionMD/Resubscribe/流重建）无锁 = 数据竞争；本地定向 race 可能绿、CI 全量并发才暴露——修复=读写统一走持锁 helper
first_seen: 2026-08-25
last_verified: 2026-09-26
skills: [KB维护]
evidence_refs:
  - type: doc
    ref: internal/adapter/mt5.go:178（reconnectSession 持 a.mu 写 a.token）
  - type: doc
    ref: internal/adapter/mt5.go:185（withSessionMD 无锁读 a.token）
  - type: server_fact
    source: GitHub Actions ci run 32810142491
    query: go test -race -count=1 ./... DATA RACE（write reconnectSession × read withSessionMD via Resubscribe）
    observed_at: 2026-08-25
---

## 事实/症状

CI 全量 `go test -race -count=1 ./...` 报 `DATA RACE`：

- 写侧：`reconnectSession` 持 `a.mu` 写 `a.token`（会话重连更新 token）
- 读侧：`withSessionMD` **无锁**读 `a.token`（经 `Resubscribe` 路径，`TestQuoteStream_ResubscribeSurvivesReconnect` 触发）

本地定向 race（`-run 'QuoteStream|...'`）跑 3 次全绿，**CI 全量并发负载下才暴露**——时序相关竞态，定向跑绿≠无竞态。

## 根因或策略解释

- 保护是**不对称**的：写点意识到并发（重连由流 goroutine 触发、与会话读并发）→ 加了锁；读点沿用了「初始化后不再变」的旧假设——但重连（S1/D-099/D-101）打破了该假设，读点没同步升级。
- 竞态在测试中由「Resubscribe 读 token」×「流断重连写 token」的并发交错触发，与生产同构（brokerMgr 映射新符号调 Resubscribe 时恰逢流重连）。
- 复审两次过手都没抓到：本地 race 定向跑绿（时序）+ 评审把它归类「既有风险不修」（S1 先例）——**CI 全量 race 是此类竞态的最终裁判**，本地绿不可作豁免依据。

## 处置

- 修复模式：**读写统一走持锁 helper**（写点已持锁；读点全部改 `getToken()` 持同一锁读）。替换点：withSessionMD / Resubscribe 空值检查+请求 Id / resubscribeAll / runQuoteStream OnQuote 请求 Id / Token() 委托。
- 锁序自检：helper 只取锁无回调；无 A→B 反向路径；写侧持锁期间不调读 helper → 无死锁。
- 验收标准：**CI 同命令 `go test -race -count=1 ./...` 本地连续 3 次全绿**（时序 bug 必须复跑）。
- 评审教训：任何「既有风险不修」的裁定，若 CI 可抓到，必须先在 CI 全量 race 验证再定案。

## 证据

- check: CI run 32810142491（102392d）失败：`WARNING: DATA RACE` write mt5.go:178 reconnectSession × previous read mt5.go:185 withSessionMD
- result: 本地 `go test -race ./internal/adapter/ ./internal/autotrade/ ...` 3 次绿（时序相关）；D-104 P0 返修② 修复（getToken 统一持锁）
- ref: 会话 2026-08-25（D-104 P0 部署 + CI 失败处置）

## 反证条件

- check: 修复后 CI 全量 race 再跑
- falsifies_when: 全量 race 仍报 token 竞态 → 修复漏读点

## 适用边界

- 一切「会话/凭证/连接状态」字段：初始化后不变假设被重连/热更新打破时，读点必须同步加锁。
- 相关：[[grpc-server-stream-reconnect-on-break]]（重连引入并发写）、[[construction-claim-needs-machine-check]]。
