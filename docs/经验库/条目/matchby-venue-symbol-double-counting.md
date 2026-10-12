---
schema_version: 2
id: matchby-venue-symbol-double-counting
kind: pitfall
risk: high
status: verified
keywords: [broker position 匹配, venueKey+symbol, double-counting, positionId, enrichPositions, matchBrokerLegs]
scope: [dashboard, enrichPositions, matchBrokerLegs, broker_position]
summary: 按 venueKey+symbol 匹配 broker position 时，同 venue+symbol 跨 position 的 broker orders 会 double-counting（#290/#291 共享 277 SELL AUDJPYm 腿→floating/swap/net 多算一倍）；必须加 positionId 唯一约束 + enrichPositions 调 annotatePositionID 标注
first_seen: 2026-08-27
last_verified: 2026-08-27
skills: [事实核验]
evidence_refs:
  - type: rpc
    desc: "ListPositions #290：修复前 net=-3.027（double-count 277 腿），修复后 net=+0.036（正确）"
  - type: code
    desc: "service_positions_enrich.go matchBrokerLegs 加 posID 参数 + bp.PositionId==posID 约束"
  - type: commit
    desc: "6938ba8 D-117 修复（2026-08-27）"
related:
  - d095-enrich-positions
  - frontend-recompute-overwrites-venue-authority
---

## 症状
#290 floating=-6.114（实际应 -3.057+2.932=-0.125），net=-3.027（实际应 +0.036）。
#291 同样 double-counting。自 D-095（2026-08-21）起一直错，D-117 回本周期计算暴露。

## 根因
matchBrokerLegs 按 venueKey+symbol 匹配 broker position：
```go
if bp.VenueKey == leg.Venue && bp.Symbol == leg.Symbol {  // 缺 positionId 约束
```
#290 和 #291 都有 277842155:mt5 AUDJPYm SELL 腿，broker positions 有两条同 venue+symbol 的记录
（分属 #290/#291），匹配时两条都被计入 #290 的 floating/swap → double-counting。

## 教训
- 按 venueKey+symbol 匹配只适用于"每 position 唯一腿"的场景；同 venue+symbol 跨 position 共享腿时必须加 positionId 约束
- broker position 的 positionId 由 annotatePositionID 标注（(venueKey,ticket)→posID 索引）
- enrichPositions 用 bpm.Snapshot() 拿到的是**未标注 PositionId** 的 broker positions——必须调 annotatePositionID 标注后才能用 positionId 匹配
- D-095 测试用例每 position 用不同 venue+symbol，掩盖了 double-counting bug——测试需覆盖同 venue+symbol 跨 position 场景

## 修复
D-117：matchBrokerLegs 加 posID 参数 + bp.PositionId==posID 约束；enrichPositions 调 annotatePositionID 标注 brokerSnap。
