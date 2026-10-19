---
schema_version: 2
id: is-not-distinct-from-no-index-pushdown
kind: pitfall
risk: medium
status: verified
keywords: [IS NOT DISTINCT FROM, 索引下探, COALESCE]
scope: [pgstore, sql, index, perf]
summary: IS NOT DISTINCT FROM 不走索引下探——NULL 安全等值须 COALESCE 形+同形表达式索引（EXPLAIN 验证）
first_seen: 2026-10-19
last_seen: 2026-10-19
---

## pg 谓词：`IS NOT DISTINCT FROM` 不享索引下探

- **实证**：rpc-perf 复审实测 LATERAL 探针用该谓词走 venue-only 索引滤 1.6K 行/键=2.23s 反劣于基线；订正=`COALESCE(col,'')` 等值+同形表达式索引（migrations/1056）。
- **规则**：凡 NULL 安全等值谓词需索引下探时，必须用 COALESCE 形或等价表达式索引配对，EXPLAIN 验证 Index Cond 覆盖（收口注记㊿）。
