```yaml
---
schema_version: 2
id: config-map-default-overrides-fallback
kind: pitfall
risk: high
status: verified
keywords: [config, map, default, fallback, commission, 佣金, 优先级, 未生效, wiring]
scope: [config, wiring, evaluator, commission]
summary: config map 默认值优先于回退常量，"改动回退值"不会生效——必须改 map 或显式删 key
first_seen: 2026-08-30
last_verified: 2026-08-30
skills: [事实核验]
evidence_refs:
  - type: decision
    ref: D-159
  - type: commit
    ref: 6fef860
  - type: doc
    ref: cmd/core/wiring.go:37-40
  - type: server_fact
    source: postgres
    query: SELECT commission_cost, count(*) FROM opportunities WHERE venue_buy LIKE '%okx%' OR venue_buy LIKE '%binance%' OR venue_buy LIKE '%gate%' GROUP BY commission_cost
    observed_at: 2026-08-30T14:00:00Z
    server_revision: ""
    response_hash: ""
---
```

## 事实/症状

D-145 施工称 "CEX taker commission 10→5"（改 `CommissionCEXTakerBps` 默认值），但生产 opportunities 表实证 CEX×CEX commission_cost=20.0（10×2 腿），改动未生效。D-145 对拍记录 "comm=20（5×4）" 推算有误。

## 根因或策略解释

`commissionResolver`（wiring.go:27-42）解析顺序：
1. CEX 腿：先查 `cfg.CommissionTaker[venue]`（per-venue map）
2. map 命中 → 返回 map 值（三所 okx/binance/gate 默认 10.0）
3. map 未命中 → 回退 `cfg.CommissionCEXTakerBps`（默认 5.0）

**陷阱**：三所默认值 10.0 已在 map 中，**永远不会回退到 `CommissionCEXTakerBps`**。改回退常量=无效改动。要让改动生效必须：
- 改 map 中对应 venue 的值，或
- 显式删 map key 让回退生效，或
- 改 map 默认值加载逻辑

## 处置

W4a evaluator 重定稿时纠正：改 `CommissionTaker` map 默认值（三所）或显式设为 5.0，而非改 `CommissionCEXTakerBps` 回退常量。STATE.md D-145 行已标注纠偏。

## 证据

- check: `grep CommissionTaker cmd/core/wiring.go` → line 37-40 优先 map
- check: `SELECT commission_cost, count(*) FROM opportunities ... GROUP BY commission_cost` → 20.0×79 + 10.3×44
- result: 生产有效 CEX taker=10bps（非 5），D-145 "10→5" 未生效
- ref: D-159 W0 T6, commit 6fef860, wiring.go:37-40

## 反证条件

- check: 改 `CommissionTaker` map 三所值为 5.0 后重跑 evaluator，opportunities 新行 commission_cost 应=10.0（5×2）
- falsifies_when: 新行仍=20.0 则结论失效（另有覆盖路径）

## 适用边界

适用于任何 "config map + 回退常量" 双层配置模式。改回退常量前必须确认 map 默认值不会优先覆盖。Go map 零值判断 `if v, ok := map[k]; ok` 的 ok=true 分支会拦截所有回退。
