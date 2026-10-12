---
schema_version: 2
id: parallel-number-collision-check-three-places
kind: pitfall
risk: high
status: verified
keywords: [撞号, D#, 编号, 迁移版本号, 并行席位, 立项, decisions.md, STATE.md, migration]
scope: [多席位并发仓库, 立项流程, migrations]
summary: 多席位并行节奏下编号取号撞车成高频形态（单日三案）——D#/迁移版本号取号必须三处同查（decisions.md+STATE.md+git log --grep；迁移另查 migrate.go 清单与 schema_migrations），撞号处置先例=施工在飞者保号、另一方改号留痕
first_seen: 2026-08-28
last_verified: 2026-09-24
skills: [KB维护]
evidence_refs:
  - type: decision
    ref: D-127
  - type: decision
    ref: D-412
  - type: decision
    ref: D-425
  - type: commit
    ref: e18c9e28
---

## 事实/症状

多席位（Claude/Devin/ZCode 并行）快节奏立项下，编号撞车从偶发变高频：2026-09-23 单日三案——D-412（扫描器四修 vs 授权保底兜底）、D-421（日内轨 vs CEX 成交三态，对方 21:02 已派施工）、migrations 0131（D-420 与 D-424 各自取 131，后到者被 schema_migrations isApplied 跳过=表永不建）。前史 D-127（预留号被占）。

## 根因或策略解释

各席位独立顺延取号，而「号已占用」的信号分布在三处且**异步**：decisions.md 条目（可能滞后数小时）、STATE.md 行（派单即翻行，往往最早）、git log（施工 commit）。只查任一处必漏。迁移版本号另有第四处：pgstore/migrate.go 显式清单——文件名改名不够，清单 version 整数撞号会被 isApplied 静默跳过（比 D# 撞号更隐蔽：不报错、表就是不建）。

## 处置

- 取号前三处同查：`grep -n "D-<n>" docs/handoff/decisions.md docs/handoff/STATE.md` + `git log --oneline --grep "D-<n>"`；迁移号加查 migrate.go 清单与生产 schema_migrations max(version)。
- 撞号处置先例（D-412/D-421）：**施工在飞者保号**，另一方改号+文档头留痕行+decisions.md 改号记录。
- 迁移撞号的特殊形态：文件名撞≠致命（embed 指令并存可编译），**清单 version 整数撞才致命**——收编集成时必核 `grep -c "{<n>},"` 唯一性。

## 证据

- check: 2026-09-23 D-421 案时间线——对方 21:02 STATE 翻「已派施工中」而 decisions.md 无条目，本席 21:22 只查 decisions.md 即取号→撞
- check: 2026-09-24 D-420 收编——migrations 双 0131 并存编译通过，isApplied(131) 跳过致 fin_cex_depth 不建（复测定量：清单 {131,×2} → 改 {132,DataPlaneVenueSQL} 后测试转绿）
- result: 三处同查法+施工在飞保号先例当日落地三案全解；迁移清单唯一性检查写入收编清单
- ref: D-412（对席改号 D-419 留痕 87b0a2d4）、D-425 改号（9380443d→22cd4142）、D-420 收编集成修正（7b729509）

## 反证条件

- check: 若引入中央取号服务或 pre-commit 取号唯一性门禁
- falsifies_when: 三处同查被门禁替代后仍有撞号漏网（说明检查面仍不全，需查第四信号源）

## 适用边界

单席位串行仓库不适用；STATE 派单节奏慢于 decisions 留痕的流程下，git log 优先级最高。
