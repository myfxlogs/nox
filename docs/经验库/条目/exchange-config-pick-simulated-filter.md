---
schema_version: 2
id: exchange-config-pick-simulated-filter
kind: pitfall
risk: critical
status: verified
keywords: [exchange_configs, simulated, demo, 实盘混选, 配置选择, ListExchangeConfigs, created_at, demo RPC]
scope: [binance, demo, cex 执行, 执行验证, 复审]
summary: 同 venue+venue_type 并存多行配置（real+demo）时，按 venue+type 取首个配置不滤 simulated——ListExchangeConfigs 按 created_at 序必先取实盘行，demo 语义路径以实盘 key 打真仓；必须显式滤 simulated=true
first_seen: 2026-09-19
last_verified: 2026-09-19
skills: [事实核验, 生产取证]
evidence_refs:
  - type: doc
    ref: internal/dashboard/service_spot_carry_demo.go ExecuteSpotCarryDemo 配置循环（D-350 @f66c453c 复审定缺陷①）
  - type: doc
    ref: internal/store/pgstore/exchanges.go:41 ORDER BY created_at
  - type: server_fact
    source: postgres
    query: SELECT id, venue_type, simulated FROM exchange_configs WHERE venue='binance'
    observed_at: 2026-09-19T09:50:00Z
    server_revision: ""
    response_hash: ""
  - type: doc
    ref: docs/D-350R1返修提示词-spot-carry六缺陷-2026-09-19-Claude.md §1 缺陷1/§3 S1
---

## 事实/症状

D-350 阶段③ `ExecuteSpotCarryDemo`（demo 手动双腿 RPC）配置选择循环只比 `Venue==venue && VenueType=="perp"` 取首个命中。生产 exchange_configs 同 venue 并存三行：id=19 spot simulated=f / id=20 perp simulated=f / id=25 perp simulated=t（demo）。`ListExchangeConfigs` 按 `created_at` 序返回 → 首个 binance+perp = **id=20 主网**（实盘 key，base `…/fapi`）；spot base 再由 `/fapi` 换段为 `…/api`=id=19 主网路径。「demo」RPC 双腿将以实盘 key 在主网下真仓单。

## 根因或策略解释

表级唯一键是 `(venue, venue_type, simulated)`——**环境位是键的一部分，不是行的标签**。任何按 venue+type 选配置的代码都在隐式做三选一，不滤 simulated = 把环境位交给 `created_at` 排序决定 = 隐式选了先创建的实盘行。fail-dangerous：路径名/函数名全带「demo」，语义与行为相反，code review 表面读不出。

## 处置

- 选择处显式滤 `cfg.Simulated == true`（D-350R1 S1：抽 `pickSimulatedConfig` helper + 无命中报 failed_precondition）。
- 测试锚：seed 真前 demo 后双行 → 断言选中 demo 行；变异摘 Simulated 过滤 → 红（TR1）。
- 复审通则：**凡「demo/模拟」命名的执行路径，必查配置选择的环境位来源**（本条即 D-350 复审 P0 实证）。

## 证据

- check: `SELECT id,venue_type,simulated FROM exchange_configs WHERE venue='binance'` → 19 spot f / 20 perp f / 25 perp t（created_at 序 20 先于 25）
- check: exchanges.go ListExchangeConfigs `ORDER BY created_at`
- result: D-350 复审缺陷①定 P0；修复=D-350R1 S1（TR1 先红后绿）
- ref: D-350 / D-350R1 / review:D350R1

## 反证条件

- check: 人为把 demo 行 created_at 改早于实盘行后跑未修复代码
- falsifies_when: 若首个命中仍为 demo 行（即排序保证 demo 恒先），则「必选实盘」结论失效——但「隐式依赖排序」本身仍是缺陷，条目降级不删除

## 适用边界

- 适用：exchange_configs 同 venue+type 多行并存（现网 binance perp 即如此）；任何新执行/查询路径按 venue+type 取配置。
- 不适用：venue+type 唯一单行的 venue（如 okx/gate 现状单行）——但新增 demo 行那一刻起即落入适用域。
- OKX 注意：`simulated` 还控制 x-simulated-trading 头（D-054/D-059），混选后果同构。
