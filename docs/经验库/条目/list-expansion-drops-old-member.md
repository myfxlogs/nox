---
schema_version: 2
id: list-expansion-drops-old-member
kind: pitfall
risk: medium
status: verified
keywords: [清单扩容, env 符号表, 挤出旧成员, diff 审查, TRX, 断供, 延迟显形]
scope: [env 配置清单变更, 符号表/白名单/路由表扩容]
summary: 扩配置清单（如 3→16 个符号）时整表替换，旧成员被静默挤出——症状延迟显形（本例两天后现价断供才暴露）。修法=清单变更必须 diff 旧值显式确认「保留集」，消费方（在本例 qbus）断供监控兜底。
first_seen: 2026-08-28
last_verified: 2026-08-28
repeat: 2
skills: [KB维护]
evidence_refs:
  - type: decision
    ref: D-126
---

## 事实/症状

D-126 P0 扩 ARB_CEX_SYMBOLS 3→16，清单漏写旧成员 TRX；8-27 部署起 TRXUSDT CEX 采集断供，8-28 #378 持仓现价 None 才暴露。

## 根因或策略解释

整表替换式清单更新没有与旧值 diff——「16 个新成员」的完整性检查掩盖了「旧 3 个是否全保留」的检查；断供无监控（下游静默空值）延迟显形。

## 处置

清单变更纪律：新值与旧值 diff，显式确认「新增集+保留集+删除集」，删除必须是有意为之；关键清单的下游断供应有告警（现价 None 类静默空值是最差显形方式）。

## 证据

- check: .env 修复前 binance symbols 16 个无 TRXUSDT；修复后 #378 currentPrice=0.3400 fresh=true
- check: 二次实证（同日 D-134，更贵）：止血写 EXCLUDE=LTCUSDT **覆盖**默认 BRENT,WTI → 油解禁 → sizing 缺陷放大 → -$2,410 真损。同型坑 10 分钟内两踩（挤出型/覆盖型），修法同一条：**改清单类配置=改安全基线，新值必须并入旧默认集（diff 三集：新增/保留/删除）**
- ref: D-126 追加修正（decisions.md）

## 反证条件

- check: 若配置系统改为增量式（append-only）清单管理
- falsifies_when: 整表替换不再可能丢成员（本条退役）

## 适用边界

一切整表替换的配置清单（符号表/白名单/路由/权限）；新增成员的验收不覆盖保留性检查。
