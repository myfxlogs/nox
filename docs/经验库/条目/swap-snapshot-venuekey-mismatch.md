---
schema_version: 2
id: swap-snapshot-venuekey-mismatch
kind: pitfall
risk: high
status: observed
keywords: [swap facts 零值校验, venue key 口径, bare venue, venueKey, HasNonZeroSwapHistory, DailySwapAccrued, position_swap_snapshots, swapFactsZeroCheck, swapDeviationGate]
scope: [autotrade, swap 安全检查, SWAPARB, CARRY, MT5]
summary: swap 安全检查（零值校验/自动与手动偏差 gate/退出）用 bare venue（ic）查 position_swap_snapshots，但快照写入用完整 venueKey（ic:mt5）——永远匹配不到非零历史，相关检查在生产数据上 silently 失效。
first_seen: 2026-09-02
last_verified: 2026-09-02
skills: [事实核验]
evidence_refs:
  - type: doc
    ref: internal/autotrade/engine_swaparb_zerocheck.go:18-22
  - type: doc
    ref: internal/autotrade/engine_position.go:220-231
  - type: doc
    ref: internal/dashboard/broker_position_monitor.go:215
  - type: server_fact
    source: postgres
    query: SELECT DISTINCT venue FROM position_swap_snapshots LIMIT 5
    observed_at: 2026-09-02T00:00:00Z
    server_revision: ""
    response_hash: ""
---

## 事实/症状

D-188 地基审计发现：`swapFactsZeroCheck`（D-165 S3）、自动 Gate 与手动偏差 Gate（D-165/D-182）以及退出对拍声称对 broker 实际 swap 做 fail-closed 处理，但生产数据上**永远匹配不到非零历史**，校验形同虚设。

具体表现：
- `swapFactsZeroCheck` 调 `HasNonZeroSwapHistory(ctx, bareVenue)`，bareVenue = `strings.Split(venueLong, ":")[0]`（如 `ic`）
- `position_swap_snapshots` 表的 `venue` 列存的是完整 venueKey（如 `ic:mt5`），由 `broker_position_monitor.go:215` 写入
- `WHERE venue = $1` 用 `ic` 匹配 `ic:mt5` → 永远 0 行 → `HasNonZeroSwapHistory` 永远返回 false
- 结果：facts=0 但实际收 swap 的 venue 被**放行开仓**（零值校验失效）

同样问题影响 `DailySwapAccrued`（`engine_position.go:220-231`、`gate_checks.go:108`、`dashboard/service_swap_deviation.go:80`）→ 自动退出、自动 Gate、手动 Gate 拿不到 broker 实际 swap → 偏差处理失效。

## 根因或策略解释

venue key 有两种口径：
1. **bare venue**：`ic`、`52993526`（不含 `:mt5`/`:mt4` 后缀）
2. **完整 venueKey**：`ic:mt5`、`52993526:mt5`（含 broker 类型后缀）

不同模块用了不同口径，没有统一约定：
- `broker_position_monitor` 写快照用完整 venueKey（`o.VenueKey()`）
- `swapFactsZeroCheck` / `DailySwapAccrued` 查快照用 bare venue（`strings.Split(venueKey, ":")[0]`）
- `facts` 表的 `venue` 列用 bare venue（`fact.Venue`）

两套口径混用导致跨表 JOIN/查询永远匹配不到。

## 处置

**待修复（D-189 P0-1）**：统一 `position_swap_snapshots` 的查询口径——所有调用方传完整 venueKey；facts 查询继续传 bare venue。禁止 bare fallback，避免 `:mt4`/`:mt5` 串数据；历史 bare 行需另立迁移。

**长期**：全项目统一 venue key 口径约定（bare vs venueKey），在 practices.md 记录规则，新代码强制遵守。

> 注：本条目 status=observed（已观察未修复），修复完成后升 verified 并补"修复实证"到证据节。

## 证据

- check: `grep -n 'HasNonZeroSwapHistory\|DailySwapAccrued\|LatestSwapSnapshot' internal/autotrade/ internal/dashboard/`
- result: 查询用 bare venue，写入用 venueKey
- ref: D-188 地基审计（2026-09-02）

## 反证条件

- check: 在生产 DB 执行 `SELECT HasNonZeroSwapHistory('ic', ...)` vs `SELECT HasNonZeroSwapHistory('ic:mt5', ...)`
- falsifies_when: 如果 bare venue 能匹配到非零历史（说明快照写入也用 bare），则口径一致，本条目失效

## 适用边界

仅适用于 `position_swap_snapshots` 表的查询路径。`facts` 表的 venue 列用 bare venue 是一致的（写入和查询都用 bare），不受此坑影响。
