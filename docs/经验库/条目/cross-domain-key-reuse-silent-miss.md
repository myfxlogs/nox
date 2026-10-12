---
schema_version: 2
id: cross-domain-key-reuse-silent-miss
kind: pitfall
risk: high
status: observed
keywords: [键域错位, key domain, legSnapshotVenue, TickVolatility24h, data_stale, margin-guard, symbol 归一化, XAUUSDm, legActionable, await_session]
scope: [arb-core, dashboard, ticks, margin_guard, exec, 跨存储域键复用]
summary: 把为 A 存储域设计的键归一化函数复用到 B 域查询，键静默不命中返 0 行——哨兵类逻辑fail-open语义下永久失明不报错；二次实证=D-495 legActionable 用 brokerSym 查 ticks(canonical 键)→await_session 死锁
first_seen: 2026-09-24
last_verified: 2026-09-28
skills: [事实核验]
evidence_refs:
  - type: doc
    ref: docs/D-430设计与施工提示词-margin-guard-tick键域修复-2026-09-24-Devin.md
  - type: doc
    ref: docs/施工-D-536-legActionable键域死锁修复-施工提示词-2026-09-28-Devin.md
---

## 现象

margin-guard 对**有持仓腿的 venue** 每 5s 刷 `data_stale`——不是数据缺失，是查询键根本不在存储域里：持仓期哨兵结构性失明（fail-open 语义下只保「不假绿」，实际什么都没评估），且无 error 可报警。

## 根因或策略解释

`legSnapshotVenue` 是给 `fin_venue_account_snapshot` join 设计的键（okx:perp→`unified` 归一 + venue 原生符号名），被复用去查 `ticks` 表——tick 域键规则完全不同（venue_type 原样 `perp|spot|mt4|mt5` + 规范符号 `XAUUSDT`/`XAUUSD`）。双域各自自洽，跨域复用即静默不命中：**SQL 查不存在的键不报错，只返 0 行**，与「真没数据」无法区分。

同类形态：凡「归一化键函数」都可能隐含其目标域假设（归一方向、符号形态、type 取值集合），函数名不体现目标域时最易被误复用。

## 处置

- 键函数命名/注释显式带目标域（`legSnapshotVenue`→快照域、`legTickKey`→tick 域），不同域各建函数不共用
- 哨兵类查询加「键域自洽测试」：fake 库按真实写侧键规则插行 → 断言读侧查得到（端到端先红后绿可钓出错位）
- 排障锚：疑似「恒 data_stale/0 行」时，先 `\d` 写侧实际键值（`SELECT DISTINCT venue_type,symbol FROM ticks`）对照读侧实参——两列实拍比读代码更快定位

## 证据

- check: `SELECT DISTINCT venue_type,symbol FROM ticks` vs `TickVolatility24h` 调用实参（'okx','unified','XAU-USDT-SWAP'）两键查询均 0 行
- result: ticks 实存 ('okx','perp','XAUUSDT')、('3001157','mt5','XAUUSD')——venue_type+symbol 双错位实证
- ref: D-430 SSOT §0 立项背景（含错复用坐标 margin_guard.go:106/179）
- **二犯实证（D-536 立案，2026-09-28）**：同一作者域内不同函数再踩——`LegActionable` 用 `leg.Symbol`（brokerSym `XAUUSDm`）查 ticks 表，而 TickSampler 写 `q.Symbol`=canonical `XAUUSD`（mt_quote_stream.go:140）→ `ticks('3001157','XAUUSDm')` 全库零行 → DISTINCT bid=0 恒判休市 → **pos502 CLOSING 死锁 27h+（市场实已开，ticks('3001157','XAUUSD') 29 tick/30min 活跃）**。修=查询侧 native→canonical 复用 `CanonicalByBrokerSymbol`（D-430 自己留的钩子，注释明写同律——同席二次踩雷=设计期未检索同类先例）；CEX 分支有 CexSymbolKey 变换而 MT 分支漏=不对称即 bug

## 反证条件

- check: 用修正后 tick 域键（perp/XAUUSDT）重查
- falsifies_when: 若修正键仍返 0 行 → 问题在 tick 采集侧而非键域错位

## 适用边界

- 本项目所有「归一化/映射键」跨函数复用场景；尤其守卫/哨兵/监控类 fail-open 读取路径（miss 不产生报错信号）
- 不适用于显式 error 返回的查询路径（键错位会以 error 暴露）
