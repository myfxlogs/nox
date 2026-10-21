---
schema_version: 2
id: baseline-edit-migration-drift
kind: pitfall
risk: high
status: observed
keywords: [迁移漂移, 基线就地改, 1000_baseline, CHECK 约束, 已应用迁移, pg_get_constraintdef, fresh vs upgraded, SQLSTATE 23514, 迁移契约]
scope: [任何对已部署迁移文件的就地编辑, 约束/枚举值扩展, 迁移契约审查, 部署后一致性核验]
summary: 就地编辑已应用的迁移文件（如 1000_baseline.sql 的 CHECK 约束加 'futures'）对生产零效果——迁移只在首次应用时落库，已部署实例不回头重放；repo 基线与生产活定义从此分叉。测试库每次从零跑迁移=永远拿到新定义，机检结构性盲区，漂移只在生产写时咬人（首撞=carry_graduations 毕业插行 SQLSTATE 23514 拒）。诊断钦定径=SG 取 pg_get_constraintdef(oid) 对拍 repo 基线；修法=新迁移 ALTER（禁再改基线、禁手工 SQL 绕迁移史），部署 runbook 加活定义对拍核。
first_seen: 2026-10-21
last_verified: 2026-10-21
skills: []
evidence_refs:
  - type: server_fact
    ref: SG nox-postgres pg_constraint cg_venue_type_ab_chk 活定义=ANY({''|spot|perp}) 无 'futures'（2026-10-21 03:4xZ 决策席亲查）
  - type: code
    ref: migrations/1000_baseline.sql:443（repo 侧含 'futures'）
  - type: log
    ref: SG nox-core 2026-10-21 03:37:08Z graduate.go:99 paper#67191 insert SQLSTATE 23514
  - type: git
    ref: ba84705ba / 9cda5a412（就地改基线引入 'futures' 的两个提交）
  - type: ticket
    ref: docs/handoff/STATE.md 票行「SG 约束漂移修」
---
