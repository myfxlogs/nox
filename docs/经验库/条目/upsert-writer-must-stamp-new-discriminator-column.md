---
schema_version: 2
id: upsert-writer-must-stamp-new-discriminator-column
kind: pitfall
risk: high
status: observed
keywords: [upsert, ON CONFLICT, DO UPDATE, 判别列, source 列, 手工/自动, 优先级规则, 覆盖语义, 同日纠错, 新列默认值, 静默覆盖]
scope: [pgstore, 记账表, 手工/自动双写方表, 任何有既有 upsert 写方的表加列]
summary: 给已有 ON CONFLICT DO UPDATE 写方的表加"来源/判别"列时，既有 upsert 的 INSERT 与 UPDATE SET 都不知道新列——手工路径覆盖 auto 行后行仍标旧来源，"自动永不覆盖手工"类优先级规则据此判断即失真，自动任务再跑会吞掉手工纠错（静默数据丢失）。修法=两个写方 SQL 显式 stamp 判别值（手工在 INSERT+DO UPDATE SET 都写 'manual'；自动用 DO UPDATE … WHERE 判别列='auto' 条件更新，永不触碰对方行）。
first_seen: 2026-09-03
last_verified: 2026-09-03
skills: []
evidence_refs:
  - type: doc
    ref: internal/store/pgstore/position_daily_income.go:26-33
  - type: doc
    ref: docs/D-211设计文档审计报告-2026-09-03.md §1 F1
  - type: decision
    ref: D-211
---

## 事实/症状

position_daily_income 既有手工写方 store.AddDailyIncome 是覆盖语义（`INSERT … ON CONFLICT (day) DO UPDATE SET …`，注释明言"同 day 重复 → 覆盖（支持同日纠错）"）。D-211 设计加 `source` 列（DEFAULT 'manual'）并规定"自动永不覆盖手工：manual 行→跳过，无行或 auto 行→upsert auto 行"。组合推演出的丢失通路：业主对 auto 行的日期手工纠错 → 数值被覆盖但该 SQL 不写 source → 行仍标 'auto' → 自动 rollup 再触达（重启补跑/补账）时按 auto 规则 upsert → 手工纠错被吞。

## 根因或策略解释

判别列的语义完整性依赖**所有写方**合作维护，而既有 upsert 写方对新列无感知（不 stamp）——默认值只在新插入时生效，DO UPDATE 分支保留旧行原值。任何"按判别列路由优先级"的规则（手工优先/自动优先/审计溯源）都建立在判别值可信的前提上；一个不 stamp 的写方就让前提破产。与 nullable-column-semantics-change-activates-latent-scan-bomb（改语义先查读方）同族——本条是写方侧姊妹坑：**加判别列先盘点并改齐全部写方**。

## 处置

- 手工写方：INSERT 列出 source='manual'，且 `ON CONFLICT DO UPDATE SET source='manual'`（两处都 stamp，签名不变）；
- 自动写方：`INSERT … ON CONFLICT (day) DO UPDATE SET … WHERE <表>.source='auto'`（Postgres 条件更新，行不匹配则 no-op）；
- 验收锚必须含双向测试：手工纠错后再 rollup 不回退 + auto 行存在时手工纠错成功且 source 翻 manual。

## 证据

- check: 读 position_daily_income.go:26-33 确认 DO UPDATE SET 无 source（列尚不存在，加列后 SQL 不改即不 stamp）；D-211 设计 §5 写入规则推演出覆盖通路
- result: 通路成立依赖三事实全为真（覆盖语义/不 stamp/auto 行可再 upsert），均有 file:line 或设计原文锚
- ref: D-211 审计 F1（43272ce）

## 反证条件

- check: 施工落地后跑双向测试（手工纠错→再 rollup→数值保持手工值）
- falsifies_when: 既有 upsert 在加列后行为已 stamp 判别值，或 rollup 永不再触达已写日（无补跑路径）——则丢失通路不成立，条目降级

## 适用边界

任何"一表多写方+判别列路由"结构（手工/自动、环境隔离、来源标记）；前提是判别列后加于既有写方之后。若表从建表起就带判别列且所有写方同步落地，本坑不触发。
