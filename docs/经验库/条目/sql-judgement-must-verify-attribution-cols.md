---
schema_version: 2
id: sql-judgement-must-verify-attribution-cols
kind: pitfall
risk: high
status: verified
keywords: [晋级判据, SQL, 归因列]
scope: [sql, judgement, schema]
summary: 判据 SQL 前先核归因列真实存在——凭想象列名=恒报错死件；判据须实读测量表
first_seen: 2026-10-19
last_seen: 2026-10-19
---

## 判据件 SQL 先核归因列存在——凭想象列名=死件

- **实证**：晋级判据 SQL 用 venue_pair_note/provenance_combo_id——positions 无 combo_id/provenance 列，查询恒报错死件。
- **教训**：判据件还须「实读测量表」非「查配置键存在」当达标。写判据 SQL 前必须对真实表结构逐列核实（`\d positions` 级实证）。
