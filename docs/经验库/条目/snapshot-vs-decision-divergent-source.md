---
schema_version: 2
id: snapshot-vs-decision-divergent-source
kind: pitfall
risk: high
status: observed
keywords: [评估卡绑定, 快照与判定异源, MaxDCIAPY, expected_apr, dci 提案, 卡验证的数≠执行的物, dsel, proposal APR]
scope: [oppmatch, dci, gate, 提案面, 评估口径]
summary: 提案评估卡/expected_apr 取聚合面极值（MaxDCIAPY 全表 30d）而执行闸实选另一档——卡验证的数≠执行的物；同族坑形=两处读同表不同行/聚合面代位实选行，静默脱节
first_seen: 2026-10-21
last_verified: 2026-10-21
skills: []
evidence_refs:
  - type: commit
    ref: cb38ce162
  - type: doc
    ref: docs/handoff/STATE.md（dci 提案 APR 脱节票 #1827 实证行）
---

## 现象

DCI 提案卡面 `expected_apr`/eval gross 与 `params` 各自由 `MaxDCIAPY`（全表 30 天极值）聚合取值，
而执行闸 `net=prem−tail` 最优选的是另一档 product——#1827 实证：卡显 33.9%、实买档 8.57%。
卡面验证通过的数不是实际执行的那份——评估/投资人决策证据失真。

## 机理

「快照面（展示/评估取值）与判定面（闸内实选行）同源不同行」：两处都读同一表，但一处取
聚合极值（MaxDCIAPY）、一处取行级实选——数据面是「对表」的、行级指向是脱节的。任何
「展示值=另一读径/另一行」的构造同此坑形（聚合代位实选、同表异行、窗口重扫漂移）。

## 修法（已落）

`dciPremiumGate`/`sellHighGate` 三返值加 `*dciSel{row,prem}`（闸内实选行指针回传）——
`expected_apr`=dsel.prem、`params.apr/product_ref/strike/tenor`=闸实选行字段、
`GrossEvidence` 标源；`proposalParams` 收 dsel 实参不再 `qs[0]` 重查（第二处脱节同根挖）。

## 识别判据（复审审项）

- 提案/评估卡展示值与判定闸取值必须**同源同行**对拍——「快照与判定异源」=退回项；
- 凡 `Max*/Min*/AVG*` 聚合面进卡面而判定面走行级实选者，结构性必脱节；
- 修形=判定面实选行指针回传展示面（判在先、显随判），禁展示面另起读径。
