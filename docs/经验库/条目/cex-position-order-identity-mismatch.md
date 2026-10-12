---
schema_version: 2
id: cex-position-order-identity-mismatch
kind: pitfall
risk: high
status: observed
keywords: [CEX, 聚合持仓, order_id, ticket, position identity, residual, LEGGED, 平仓]
scope: [exec, reconcile, CEX, perp]
summary: CEX 聚合持仓的交易对身份不能直接匹配开仓订单 ID；若沿用 ticket 回读，会把残腿误判为已平仓
first_seen: 2026-08-30
last_verified: 2026-09-02
skills: [KB维护, 事实核验]
evidence_refs:
  - type: doc
    ref: internal/exec/pipeline_legs_verify.go:21-58
  - type: doc
    ref: internal/reconcile/cex_positions.go:142-150
  - type: server_fact
    source: postgres
    query: SELECT status,count(*) FROM positions GROUP BY status
    observed_at: 2026-08-30T10:33:00Z
    server_revision: ""
    response_hash: ""
---

## 事实/症状

执行腿在 `position_legs.order_id` 中保存交易所订单 ID；CEX 持仓查询器为聚合仓位返回交易对作为 Ticket。平仓残腿校验若将两者作为同一身份比较，查询不到真实残腿。纯 CEX LEGGED 自动收平若只看订单调用无错误，也可能把未确认的残腿写成 CLOSED。

## 根因或策略解释

订单身份与仓位身份是两个不同层级：订单 ID 标识一次委托，CEX 聚合持仓通常只提供 symbol、side、数量和仓位侧，不提供单笔订单 ticket。跨层直接比较必然失配；查询出错时再对 CEX 放宽 fail-closed，会形成“本地已关闭、交易所仍有仓”的隐性裸露。

## 处置

建立统一的 CEX position identity（venue、账户、symbol、position side、数量/仓位快照），保存 client order ID 与交易所 order ID，并在平仓前按 venue+symbol+side 识别聚合仓、平仓后按仓位快照和成交回读确认残量为零；确认前不得置 CLOSED。LEGGED 自动收平必须写入 CLOSE leg、实际成交量、价格、手续费和结算结果；任一确认失败进入可重试恢复态。

## 证据

- check: `internal/exec/pipeline_legs_verify.go` 对 OPEN 腿使用 `OrderID` 构建待查 ticket；`internal/reconcile/cex_positions.go` 将 CEX 聚合仓位 `Ticket` 设为 `InstID`
- result: 两种身份不一致；本次数据库快照另有 CLOSED 持仓缺少 `realized_pnl` 的数据异常
- ref: `docs/项目全面审计与评估报告-2026-08-30.md` §5 P0-1/P0-2；`docs/D-189-P0静默故障修复-设计文档.md` §4；取证窗口 2026-08-30/2026-09-02

## 反证条件

- check: 对 Binance/OKX/Gate 各建立一笔带已知 client order ID 的最小 Demo 仓，分别部分平仓、完全平仓、网络超时后回读 position 与 order 状态
- falsifies_when: 若在所有支持的账户模式下，venue 能稳定返回可与本地 OPEN leg 一一对应的订单身份，且残量为零前永不返回 CLOSED，则本条当前适用范围需收窄为不支持该身份映射的 venue

## 适用边界

仅适用于 CEX 聚合持仓和订单级对账；MT4/MT5 通常提供 ticket 级持仓身份，不直接适用。该条目当前为项目级 observed 经验，不晋升全局 KB。
