---
schema_version: 2
id: aggregate-fanout-into-rows
kind: pitfall
risk: high
status: observed
keywords: [增量归属, 维度错配, venue级汇总, 逐行扣减, 扇出, 申购流, earn_yield, 聚合粒度, 幂等复制, 相同金额跨键重复]
scope: [增量/差分归属公式, 对账解释项扣减, 任何 row=f(row_prev, 聚合量) 形态的账本/统计写径, 多币种多维度池记账]
summary: 归属公式逐行执行却引用了比行更粗粒度的聚合量——`delta=equity−prev−flow` 按币种行跑，`flow` 却是 venue 级申购汇总，导致 USDT 的 $351.64 申购被从 binance:earn 全部 85 个币种行各扣一遍（每符号恰好一笔 −351.64，累计 −$30,367 假收益）。聚合量维度必须与逐行公式维度严格对齐，否则聚合量被应用 N 次（每行一次）。
first_seen: 2026-10-17
last_verified: 2026-10-17
skills: [协议面取证]
evidence_refs:
  - type: code
    ref: internal/api/dashboard/earn_yield.go attributeEarnYield（EarnSubscribeSum(ctx, r.Venue, …) 无 ccy 参）
  - type: decision
    ref: STATE 票「binance earn_yield 归属扇出修复」根因段
fix_and_prevention: ①聚合量与行维度对齐——逐行公式的每个输入取该行同维度值（EarnSubscribeSum 加 ccy 参，USDT 申购只扣 USDT 行）；②检测信号=**相同金额跨大量键重复**（每 symbol 恰好 −351.6x）——出现即扇出形态，查公式输入的粒度；③多币种/多维度池的账本写径，评审时逐输入核粒度对齐（本坑 okx/gate 无恙纯因单币种池掩盖——单币种环境通过≠多币种正确，掩盖性通过本身是坑）；④测试锚须含多币种池场景（单币种 fixture 测不出扇出）。
notes: 业主问「活期收益能查吗」抓出；初判「JOIN 扇出/JSON 错」均不准——读码定谳为公式输入粒度错配（协议面取证 skill ④又一实证：先读码再下结论）。
---
