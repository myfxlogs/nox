---
schema_version: 2
id: server-facts-over-docs
kind: pitfall
risk: high
status: verified
keywords: [事实优先, 服务器事实, 文档过期, STATE 过期, proto 表象, 只读探针]
scope: [workflow, exec, ops]
summary: 只读文档/proto/函数名/旧 STATE 就宣布「当前状态」→ 结论过期或错误；当前事实唯一来源 = 服务器/数据库/venue 只读返回，文档与旧 KB 只是历史证据
first_seen: 2026-08-15
last_verified: 2026-09-11
skills: []
evidence_refs:
  - type: decision
    ref: D-059 实施修正③
  - type: doc
    ref: ~/kb/pitfalls/2026-08-15-verify-before-adopt
  - type: server_fact
    source: postgres
    query: SELECT round(sum(realized_pnl)::numeric,2) FROM positions WHERE closed_at >= date_trunc('day',now());
    observed_at: 2026-08-21
---

## 事实/症状

只读文档/proto/函数名/旧 STATE 就宣布「当前状态」→ 结论过期或错误。实例集：

1. D-059 实施修正③：「Binance perp 需单独 futures testnet key」系探错域错结论，被业主纠正复核后推翻——只读代码得出错误结论，实际 demo-fapi.binance.com 同 key 可用。
2. 2026-08-21 审计：旧观察（当日 -94.35）与同日新鲜只读探针（-95.22）漂移——STATE 级事实同日内即过期。
3. ~/kb `pitfalls/2026-08-15-verify-before-adopt`（repeat 4，跨项目重现）。
4. 2026-09-11 D-278 S4 复审两连击（业主纠正）：①见 `fin_cmc_positions.evaluation_bid_price` 恒 0 → 无证据断言「CMC 对空头不返回 bid 是数据源特性」——实查 `fin_cmc_prices`：`src=cmc_rest` 行 bid/ask/mid 三值齐全，数据早就在库，是取价取错源（正确修法=换源 `LastCMCPriceMidWithTime`，而非下游兜底链补丁）。②「现价应该来自行情快照」句式=未验证猜测（当时未查 schema）。「为什么没数据」诊断顺序：先查数据是否已在库（换表/换源），再谈采集缺口，最后才谈兜底。
5. 2026-09-24 D-466（业主纠正变体）：「服务端数据」**严格按腿归属**——同名 symbol 的其他 venue 数据不属于该腿的服务端事实。OKX XAU-USDT-SWAP 实结 8h 期，但持仓卡倒计时取 Binance fundingInfo 同名 `XAUUSDT` 的 4h 期 → 跨 venue 污染显示错值；`cexStepMeta` 同款（Binance exchangeInfo stepSize 表+`cexStepSizeFallback` 硬编码表被用于非 binance 腿）。同名≠同值：结算期/步长/费率都是 venue-local 事实。

## 根因或策略解释

文档、proto 定义、函数名和 STATE.md 都是某一时刻的快照。运行时状态（持仓、余额、配置生效值、TradeMode、funding、swap）随时变化，文档更新滞后于实际状态。仅凭文档判断当前状态 = 用过期信息做决策。

## 处置

当前事实唯一来源 = 服务器/数据库/venue 只读返回。声明运行时状态前必须执行只读探针（SQL 查询、RPC 调用、broker API 查询），将结果作为证据。文档与旧 KB 只是历史证据和假设来源，不能替代当前事实。

**venue 归属律（2026-09-24 业主令补强）**：「venue 数据」指**该腿所属 venue 自己返回的数据**——同名 symbol 在别家 venue 的值（结算期、步长、费率、规格）一律不算数；同 canonical 不豁免。硬编码表同样禁止（它本质是「我猜 venue 都一样」）。查不到本 venue 真值 → 诚实空态/warn 回退，不得跨 venue 借数。

适用范围（设计 §1.2）：运行时状态声明（持仓/余额/配置生效值/策略状态/TradeMode/funding/swap/部署状态/迁移版本）强制探针；纯文档解释、纯函数行为、历史根因、架构决策历史理由、已固定代码规则豁免。

## 证据

- check: `SELECT round(sum(realized_pnl)::numeric,2) FROM positions WHERE closed_at >= date_trunc('day',now());`
- result: -95.22（2026-08-21 只读探针，与旧观察 -94.35 漂移）
- ref: D-059 实施修正③ + ~/kb/pitfalls/2026-08-15-verify-before-adopt（repeat 4）

## 反证条件

- check: 寻找可长期仅凭文档/代码判定的「运行时状态」类别且与服务器零漂移
- falsifies_when: 出现可长期仅凭文档/代码判定的「运行时状态」类别且与服务器零漂移 → 收窄 scope 至已证漂移的状态类（持仓/配置生效值/TradeMode/funding/swap）

## 适用边界

运行时状态声明（持仓/余额/配置生效值/策略状态/TradeMode/funding/swap/部署状态/迁移版本）。纯文档解释、纯函数行为、历史根因、架构决策历史理由、已固定代码规则不强制探针（设计 §1.2）。
