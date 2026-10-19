---
schema_version: 2
id: timescaledb-hypertable-conversion-constraints
kind: ops
risk: medium
status: verified
keywords: [timescaledb, hypertable, create_hypertable, facts_pkey, 原生分区, 压缩, companion, _compressed, drop_chunks, retention policy, pg16]
scope: [pgstore, 存储层, SG 部署, 迁移]
summary: TimescaleDB 2.30.2 实测四坑：单列 PK 拦 create_hypertable、原生分区表拒直转、压缩存储在 _hyper_*_compressed companion（chunk 侧 pg_total_relation_size 严重低报）、vanilla 镜像回滚被 shared_preload_libraries 卡启动
first_seen: 2026-10-19
last_verified: 2026-10-19
skills: []
evidence_refs:
  - type: server_fact
    source: postgres
    ref: 5436 评估实例（timescale/timescaledb:latest-pg16，tsdb 2.30.2/pg16）10-19 实测全套
  - type: doc
    ref: docs/handoff/STATE.md TimescaleDB 评估票行（施工产出节）
  - type: doc
    ref: internal/store/pgstore/facts.go:187 EnsureMonthPartition（hemtable 后可删的手动分区径）
repro_or_symptom: ①`create_hypertable('facts','ts')` 报 cannot create a unique index without the column "ts"（单列 facts_pkey 拦路）；②`create_hypertable('ticks',...)` 报 "It is not possible to turn partitioned tables into hypertables"（原生 RANGE 分区表硬拒）；③压缩后 chunk 关系仅剩壳（14 片共 784kB），真数据在 `_hyper_N_M_chunk_compressed` companion（82MB/500 万行随机值）；④装过 timescale 的数据卷回换 postgres:16 镜像→shared_preload_libraries 引用缺失库拒启
root_cause: hemtable 唯一索引必须含分区列；原生 PG 分区与 hemtable 分区两套机制不兼容（唯一出路=新表+搬数+换名）；压缩引擎把行存搬进 companion hypertable；镜像内 init 的 timescaledb-tune 会把 timescaledb 写进 postgresql.conf 的 shared_preload_libraries（数据卷持久化，镜像回退不回滚配置）
fix_and_prevention: ①转换前置=DROP 单列 PK（facts.id 无 FK 被引，已核）或改复合 (id,ts) ②ticks 转换路径=CREATE 同构空表→create_hypertable→INSERT SELECT→RENAME 换名（评估实测 1.44M 行秒级）③空间实测一律打库级 pg_database_size 或 sum(_compressed companion)，勿信 chunk 侧单值 ④部署预案写明回滚：换回 vanilla 前先 psql 改 postgresql.conf 剔 timescaledb 再启（或弃卷重建）；SG pg 2500m 内存帽须以命令行 GUC 覆盖 timescaledb-tune 的宿主内存假设
notes: 基准与容量结论（500 万行随机值数据集，10 次中位）：压缩态 82MB vs vanilla 2198MB≈27×；压缩后定向聚合读大幅提速（LatestFacts 带 kind 130ms→3.7ms）、小窗口查询有固定逐片开销（+0.3~3ms）；未压缩 hemtable 全面微劣于 vanilla。写路径 COPY 双侧通、压缩 chunk 上 COPY/INSERT 均收。200k 行 COPY：ts 2.4s vs vanilla 3.0s。迁移链 232 版零冲突重放（pgstore/autotrade/scan 三套件 5436 全绿）。
---

## 现象

评估票四步实测各撞一坑：PK 拦转换、原生分区拒转、体积看错地方、回滚路径暗雷。

## 根因或策略解释

见 summary 四条；均为 TimescaleDB 机制固有行为，非版本缺陷（2.30.2/pg16）。

## 处置

评估票只测不改。部署影响面结论与回滚预案已入 STATE 票行施工产出节；转换 DDL 前置条件供后续采用票直接引用。

## 证据

- result: facts hemtable 化（删 PK 后）92×1d 片/500 万行无损；7d 片重测 14 片
- result: drop_chunks(72h) 101 片→4 片 50ms；add_retention_policy/add_compression_policy 挂讫
- result: cagg funding 1h（refresh 2.3s）与直扫读数逐位一致（avg 0.00582217/n=21）

## 反证条件

- check: 新版 timescaledb 放开原生分区直转或唯一索引约束
- falsifies_when: 上述任一机制变更落地（升级时复查本条）

## 适用边界

NOX facts/ticks hemtable 化、SG 镜像替换评估、任何「拿 pg_total_relation_size(chunk) 说压缩率」的断言。
