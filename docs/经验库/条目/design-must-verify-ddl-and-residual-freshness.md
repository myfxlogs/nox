---
schema_version: 2
id: design-must-verify-ddl-and-residual-freshness
kind: pitfall
risk: high
status: verified
keywords: [DDL 约束, 唯一索引, NOT NULL, 设计前置, 双轨并存, 挂起语义, 残留清单, 时效性, git log -S, 写入可行性]
scope: [任何涉及"向既有表新增写路径/双轨并存/挂起行"的方案设计, 引用历史 D# 残留清单的二次设计]
summary: 方案设计两条前置核验缺失会让方案结构性不可行——①向既有表设计新增写入语义（并存/挂起/多行）前必须先查目标表 DDL（唯一索引/NOT NULL）：per-leg 日粒度唯一索引会让"actual 与 estimated 双轨并存"每天撞约束，position_id NOT NULL 会直接否决"无匹配挂起行"；②引用旧 D# 记录的"残留/硬编码清单"前必须重核当前码（git log -S <符号>）：后续施工可能已把清单里的大半残留修掉，照旧引用=范围失真、验收锚空转。
first_seen: 2026-09-18
last_verified: 2026-09-18
skills: []
evidence_refs:
  - type: doc
    ref: docs/D-334三新发现解决方案-2026-09-18-Claude.md §8（v1→v2 三修正：income_events 返工/Gate 收窄/双装配点）
  - type: sql
    ref: pg_indexes WHERE tablename='position_cashflows'（per_leg_uniq+event_key_uniq 实查）
---
