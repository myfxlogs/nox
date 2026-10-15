---
schema_version: 2
id: bounded-list-anchor-lookup-clamp
kind: pitfall
risk: high
status: verified
keywords: [ListDCIOrders, limit clamp, LIMIT 20, 锚点查询, invest=0, 得额当损益, dci_exercise_disposal, 静默截断]
scope: [pgstore, dciexec, 钱径锚点, 分页查询]
summary: 拿有界 ListXxx(limit=N) 当 id 锚点查找被 store 层静默 clamp 截断（500→20）致锚永空， invest=0 使卖回得额 351.94 被当净利入账虚增 ~358——分页列表禁当身份查找用
first_seen: 2026-10-15
last_verified: 2026-10-15
skills: [生产取证]
evidence_refs:
  - type: server_fact
    ref: SG income_events tran_id=117 dci_exercise_disposal=+351.94443510（应 -6.0556）；fin_dci_order 31 行而 ListDCIOrders LIMIT 20 截掉 id=11
  - type: doc
    ref: internal/store/pgstore/dci_order.go ListDCIOrders limit>200→20 clamp
  - type: decision
    ref: decisions.md D-542（DCI LOT_SIZE+pending_disposal 清扫）
repro_or_symptom: 循环遍历 ListXxx(limit) 结果按 id 匹配锚点记录，目标行落在截断窗口外→查无→静默降级零值参与钱径计算（得额被当损益入账）
root_cause: 分页列表语义=「取前 N 行」非「全集」；clamp 在 store 层静默生效，调用方无从察觉；且「查无锚」与「读败」共用 invest=0 降级路径，把两种异常同化为零值
fix_and_prevention: ①锚点查询必须走 XxxByID 单点查（DCIOrderByID 已入库），禁拿有界列表当查找②store 层 clamp 须在签名/注释显式③「查无锚」独立处置径：卖回照做（真账）但 income 不入账+告警明示待人工补记，不许静默退化零值④新增分页 clamp 时在调用方注释处锚明
notes: 同型病灶可查面：任何 list+循环按 id 找锚的钱径代码；检测信号=锚字段恒定零值但源表确认有行
---

## 现象

D-542 清扫件首拍：留仓 BTC 卖回得 351.94 USDT，但 `dci_exercise_disposal` income 记 +351.94 而非 -6.06——`ListDCIOrders(ctx,500)` 被 clamp 到 LIMIT 20，id=11 在 31 行表的窗口外，`invest` 静默归零。

## 根因或策略解释

两个缺陷叠加：①有界列表被当身份查找用，clamp 截断后锚永空；②「查无锚」与「读败」收敛到同一 `invest=0` 降级分支，无独立处置径——得额≠损益的语义边界被抹掉。

## 处置

改走 `DCIOrderByID` 单点查；锚缺时 income 不入账+关行文案与告警明示「原单锚缺待人工补记」；生产幻影行 tran_id=117 修 -6.0556。测试 `TestD542_SweepUnanchored` 钉死语义。

## 证据

- check: SG psql `income_events tran_id=117` / `fin_dci_order` 行数与 LIMIT 截断面
- result: 修复前 +351.94（虚增 ~358）→ 修复后 -6.0556 真损益
- ref: commit bff9080ff；D-542

## 反证条件

- check: 若 ListXxx 改为全集返回且签名明示语义，单点查是否仍必要
- falsifies_when: 移除 clamp 的全集列表在实测中等价于 ByID 查询（当前不成立——clamp 是 store 层有意防御）

## 适用边界

凡「先列后按 id 找锚」的钱径/账本代码同受此规则约束；统计/展示面用列表无碍。
