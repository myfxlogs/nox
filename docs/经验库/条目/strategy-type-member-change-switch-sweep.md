---
schema_version: 2
id: strategy-type-member-change-switch-sweep
kind: pitfall
risk: high
status: verified
keywords: [新策略类型, 枚举成员变更, switch 漏型, 同类对象一致性, IsCarryType, ExpectedUsd, hasCashflowKinds, default 吞掉, 普查, grep 既有成员]
scope: [Go 多型分派代码, 验证统计, 结算质量, 回填脚本, 复审方法论]
summary: 新增策略类型（枚举成员）时既有按型分派的 switch 不会自动跟上——D-300 加 XASSET_CARRY 只改了 IsCarryType，ExpectedUsd/hasCashflowKinds/backfill expectedUsd 三处静默落 default（生产实证=该型结算恒 skipped「expected_usd<=0」+现金流质量检查失效）；修法=新成员入库时 grep 既有成员串全库普查按型分派点并逐个定性语义归属
first_seen: 2026-09-18
last_verified: 2026-09-18
skills: [验证与交接]
evidence_refs:
  - type: decision
    ref: D-331
  - type: doc
    ref: internal/verification/verification.go:36
  - type: doc
    ref: docs/handoff/LOG.md「D-331 施工」段
---

# 新枚举成员变更后的按型 switch 普查法

## 症状

新策略型（如 XASSET_CARRY）上线后主链路正常，但旁路统计系统性失明：验证统计对该型恒
`skipped, skipped_reason='expected_usd <= 0'`、现金流质量检查对缺失 funding 不判 unknown、
回填脚本产 0 期望。代码层特征=多处 `switch posType/typ` 的 `case` 列表缺新成员 → 落
`default`，无任何报错。

## 根因

「类型集合」是隐式多点共识：每处 switch/case 列表/[]string 清单都是一份独立拷贝（P3 违例面）。
新成员只在「当时改到的那几处」登记（D-300 只改 IsCarryType），其余拷贝静默过期——同类对象
一致性维度（D-157 ①）的经典漏改产物。

## 修法（普查法）

1. 新成员入库的同一个工作包里，`grep -rn '"<既有成员串>"' --include='*.go'`（如
   `"PERP_FUNDING"`）列出全部按型分派点；
2. 逐点定性：**该型是否属于此分派的语义域**（如 ExpectedUsd 的 FUNDING 族=年化百分点口径
   ——XASSET net_bps 同语义→属；netStoreGate 每型独立阈值→已有 case；退出判定 XASSET 有
   专属路径→不属）——语义不属于的**不要机械补型**，列报审由决策席裁；
3. 同语义漏点全补+锚先红后绿；异语义存疑点显式登记移交。
本次普查实证：3 处补型（verification/settlement/backfill）+3 处异语义位点移交
（metaForType 年化/close-scan 策略清单/accumulator 年化均值）。
