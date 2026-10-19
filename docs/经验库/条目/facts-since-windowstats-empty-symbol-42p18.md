---
schema_version: 2
id: facts-since-windowstats-empty-symbol-42p18
kind: pitfall
risk: medium
status: fixed
keywords: [42P18, could not determine data type, factSymbolPred, FactsSince, FactsWindowStats, symbol 空, 参数化, pgx]
scope: [pgstore, facts, 账号级 fact 读取]
summary: factSymbolPred 双字面分派（D-574）把 $3 从 SQL 拿掉但调用侧仍绑 4 参——FactsSince/FactsWindowStats 传 symbol="" 即 42P18 硬错；现役调用面全非空（10-19 审计），属潜在陷阱非现症
first_seen: 2026-10-19
last_verified: 2026-10-19
skills: []
evidence_refs:
  - type: doc
    ref: internal/store/pgstore/facts.go:87-92
  - type: doc
    ref: internal/store/pgstore/facts.go:244-251
  - type: server_fact
    source: postgres
    ref: 5436 评估实例实测：FactsWindowStats(ctx,"equity","acct_00","",7d) → ERROR 42P18 could not determine data type of parameter $3
repro_or_symptom: symbol="" 时 factSymbolPred 返回 `symbol IS NULL` 字面（$3 不在 SQL 文本），pgx 仍按 4 参绑定向 PG 发送——扩展协议 Parse 阶段 $3 未被引用且类型不可推断，报 42P18；同形（symbol 空、$3 缺位、4 参）在 FactsSince 同炸
root_cause: D-574 双字面改造只改了 SQL 文本分支，未同步收窄绑定参数个数——SQL 占位符集合与参数向量失配；此前无人踩中因全部现役调用点恰好传非空 symbol
fix_and_prevention: ①最小修=factSymbolPred 空支路同时吞掉该位参数（返回谓词+指示调用侧少传一参），或空 symbol 时把参数槽改回恒真字面（`($3='' )`）保参数个数恒定 ②凡按入参裁剪 SQL 片段的 helper 必须同步裁剪参数向量，二者同源生成 ③新增调用点传空 symbol 前 grep 本条目
notes: 10-19 审计：FactsSince×6 / FactsWindowStats×2 现役调用点（marketstate/classify、oppmatch/s2bridge、scan/accrue_settle、ledger/reconcile_perp、scan/carry_universe_collect）全传 leg/canonical 语境非空 symbol——未触发现症；方向=fail-closed（硬错不脏数据）。TimescaleDB 评估票基准 harness 首次踩中。
---

## 现象

评估票基准 harness 调 `FactsWindowStats(ctx, "equity", "acct_00", "", 7d)` → `ERROR: could not determine data type of parameter $3 (SQLSTATE 42P18)`。

## 根因或策略解释

`factSymbolPred`（facts.go:87）空 symbol 分支返回 `symbol IS NULL`，SQL 文本中 $3 消失；`FactsWindowStats`/`FactsSince` 调用侧无条件绑 4 参。pgx 扩展协议下 PG 收到未被 SQL 引用的参数且无法推断类型 → 42P18。

## 处置

本票为评估票不动代码（禁线）。潜在陷阱记档；修复归后续票（最小修=谓词与参数向量同源生成）。

## 证据

- repro: 5436 评估实例 `FactsWindowStats(...,symbol="",...)` 100% 复现 42P18
- audit: 现役 8 个调用点全非空 symbol（grep 实拍 10-19），未触发现症

## 反证条件

- check: 任一生产路径以 symbol="" 调用且未报错
- falsifies_when: 若 pgx 改走 simple protocol 或参数槽恒定化修复落地，本条降级为历史教训

## 适用边界

`facts.go` 内按参数裁 SQL 的读取面（FactsSince/FactsWindowStats 及后续同形 helper）；LatestFacts 用 `($3='' OR symbol=$3)` 恒占位形态不受影响。

**修讫（10-19，`e46e84e`）**：`FactsSince`/`FundingPredWindow`/`FactsWindowStats` 三调用点参绑定随谓词分派（symbol 非空才占参位，后续占位号 len(args) 动态算）；回归测试 `TestD412b_EmptySymbol42P18` 在案。
