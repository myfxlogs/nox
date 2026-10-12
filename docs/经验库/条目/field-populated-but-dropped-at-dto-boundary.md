---
schema_version: 2
id: field-populated-but-dropped-at-dto-boundary
kind: pitfall
risk: high
status: verified
keywords: [字段丢失, DTO 转换, OrderEvent FeeUSD, BinanceOrderSnap, 采集管线, 过期注释]
scope: [数据采集管线, 事件缓存→快照转换层, CEX/MT5 手续费]
summary: 采集层字段已正确填充但在内部 DTO/snap 转换处被丢弃——外层排查只确认「采集有值」会漏掉中间转换丢字段；伴随旧注释（「费恒 REST」）更易误判。修=逐转换层 trace 字段端到端，快照类型补字段透传。
first_seen: 2026-09-30
last_verified: 2026-09-30
skills: [事实核验]
evidence_refs:
  - type: decision
    ref: D-599
  - type: commit
    ref: a6b76a79
  - type: doc
    ref: internal/exec/cex_executor_confirm.go
  - type: server_fact
    source: postgres
    query: SELECT count(*) FROM position_legs WHERE fee_usd IS NOT NULL
    observed_at: 2026-09-30T08:00:00Z
---

## 事实/症状

pos507 gross +1.40 账面盈利，实扣腿费 4.18 后净亏 −2.78。排查发现 binance 私有 WS 帧 `n`/`N`（佣金金额/币种）早已在 `BinanceUserWS.onOrderUpdate`/`onSpotExecutionReport` 解析进 `OrderEvent.FeeUSD`，但 `CexExecutorManager` 转 `exec.BinanceOrderSnap` 时该类型**根本没有 FeeUSD 字段**——值在 snap 边界静默丢弃，执行确认链仍回退 REST 查费。类似地 gate 历史回填路径把 `hp.Commission` 写进 cashflows 却漏写腿行 `fee_usd`（pos505 实证：cf −2.08 在档、腿 NULL）。

## 根因或策略解释

DTO/快照类型是窄化转换点：下游只取它声明的字段。采集层先行、消费层字段后加的演进中，旧注释（「费恒 REST——无 FeeUSD 字段」）定格了写时的假设，字段后来被加进上游结构体但快照未同步，注释与实际不再相符却无人回验。

## 处置

逐转换层 trace 字段端到端（解析→缓存→snap→Fill→leg→cashflow→position）；snap 类型补 `FeeUSD` 透传；采集按币种门控（`N`∈USD 系才计 USD 费，非 USD 系置 0 交兜底通道）；历史回填路径与正常路径同字段写入。排查「采集有值但落库 NULL」时先查中间 DTO 的字段集，别只查首尾。

## 证据

- check: `git show a6b76a79`——BinanceOrderSnap 补 FeeUSD + binanceSnapEval 事件费优先 + placeGateRaw 事件快照补费 + backfillCloseFromHistory 落 hp.Commission
- check: pos507/502/506 leg fee_usd 实际值在档（okx 接线后 fill 全带费）
