---
schema_version: 2
id: ambient-db-rows-mask-missing-fixtures
kind: pitfall
risk: high
status: verified
keywords: [测试, fixture, seed, ambient, 共享测试库, CI 鲜库, 遮蔽, fin_cex_spread]
scope: [test, CI, pgstore]
summary: 测试依赖 ambient 数据行未 seed——共享测试库残留行遮蔽缺 seed 缺陷，本地全绿 CI 鲜库必挂（TestD514 漏 seed fin_cex_spread BTC 行实证）；验收锚=全新空库复跑
first_seen: 2026-10-15
last_verified: 2026-10-15
skills: [KB维护]
evidence_refs:
  - type: code
    ref: TestD514_PremiumGate——dciPremiumGate 读 LatestSpreadSpotRef(BTC) 查 fin_cex_spread，测试未 seed 该行
  - type: server_fact
    ref: CI run 38692966418 挂「BTC 现货参照缺」，本地共享库残留行遮蔽全绿
  - type: decision
    ref: STATE 推送者自检纪律（10-15 业主令——push 后 gh run list 自查）
repro_or_symptom: 代码新增某表/某键的读取依赖，测试只断言行为不 seed 依赖行——本地跑过=共享库里有别人留下的行；CI 起全新空库→读取者 fail-closed 报缺→断言挂
root_cause: 测试库共享且不清空，ambient 行充当隐形 fixture；测试作者未把「输入事实全集」当测试件的一部分
fix_and_prevention: ①测试前置显式 seed 全部依赖行（表清空语义明确后 seed，禁靠 ambient）②新依赖读出现时同步审测试 seed 面③验收锚用全新空库 DSN 复跑而非现有共享库④push 后 gh run list 自查 CI 转绿才算闭环
notes: 检测信号：本地绿 CI 红且挂点为「参照缺/查无行」类 fail-closed 报错=疑似 ambient 遮蔽
---

## 现象

TestD514_PremiumGate 本地绿、CI 挂「BTC 现货参照缺」——dciPremiumGate 依赖 fin_cex_spread BTC 行，测试未 seed，共享库残留行遮蔽。

## 根因或策略解释

共享测试库 ambient 行充当隐形 fixture，输入事实不闭合的测试在脏库里永远绿。

## 处置

测试补 seed BTC spread 行（28e2af737）；CI 转绿验讫。配套纪律：推送者 push 后 `gh run list` 自查（业主令 10-15）；验收锚=全新空库。

## 证据

- check: CI 38692966418（红）→ 38694601088（绿）
- result: seed 补齐后鲜库过
- ref: 28e2af737

## 反证条件

- check: 测试库改为每测试独立建+DROP
- falsifies_when: 若测试隔离基建落地（每用例独立库），ambient 遮蔽自然消失——本坑降级为历史教训

## 适用边界

依赖 DB 事实行的测试（gate/评估/判据类读取者）；纯内存测试无此险。
