---
schema_version: 2
id: exit-formula-leg-order-inversion-hold-time-signature
kind: pitfall
risk: critical
status: verified
keywords: [退出条件, 腿序颠倒, 符号反向, currentNetBps, 开仓即平, 持仓时长分布, 策略误判死, MEANREVERT, CROSS_VENUE, 同类公式交叉核对]
scope: [autotrade, engine, 收敛型策略, 任何入场/退出成对公式]
summary: 退出判定公式与入场定义腿序不一致（入场 sell.Bid−buy.Ask、退出 buy.Bid−sell.Ask）→ 开仓瞬间恒满足退出 → 首轮扫描即平——症状=持仓时长中位数≈扫描 tick、胜率≈0；策略据此被判死是归因错误。侦测法=同语义公式全库 grep 对齐 + 持仓时长分布对照扫描节拍。
first_seen: 2026-09-23
last_verified: 2026-09-23
skills: [事实核验, KB维护]
evidence_refs:
  - type: doc
    ref: docs/开平仓管线逻辑审计-2026-09-23-Devin.md §1 P1-1
  - type: doc
    ref: internal/autotrade/engine_basis.go:110-113
  - type: commit
    ref: b8931f6f
  - type: server_fact
    source: postgres
    query: select type, percentile_cont(0.5) within group (order by extract(epoch from closed_at-opened_at)), sum(case when realized_pnl>0 then 1 else 0 end) from positions where status='CLOSED' and type in ('CROSS_VENUE','MEANREVERT') group by 1
    observed_at: 2026-09-23T12:30:00Z
    server_revision: "master eee7a40e"
    response_hash: ""
---

## 事实/症状

- MEANREVERT 25 单 CLOSED：持仓时长中位数 9.4 s（min 4.3 s），0 胜，Σ −$118.79；8-25 五单收盘时刻等间距 5 min（=扫描 tick），平后 5 s 再开（价差仍在）=开平循环烧费。
- CROSS_VENUE 38 单 CLOSED：中位 158 s，1 胜，Σ −$2922.85。
- strategy_stats 两行被禁用，归因写「跨 broker 基差不收敛/成本吃光」——归因错误。

## 根因或策略解释

`currentNetBps`（engine_basis.go:110）算 `buyClose.Bid − sellClose.Ask`（清算侧基差），而入场 detector/`Revalidate`/meanrevert 扫描/前端 enrich 五处全算 `sell.Bid − buy.Ask`（剩余机会价差）。开仓后清算侧基差 = −edge − 双腿点差 恒 <0 → `if spread<=0 return 0` → 0 ≤ CloseBpsThr(0.5) → 判「converged」平仓。方向完全反：真正收敛时（sell 跌到 buy 之下）反而判「未收敛」持有。自 D-070（b8931f6f，8-21）引入即反——策略从未被真实测试。

## 处置

- 修法：`spread := sellClose.Bid − buyClose.Ask`，抽共享纯函数供入场/复核/退出/展示四处共用（P3 单一真相），先红后绿三用例（entry edge 20bps 修前判平修后持有；sell 跌破 buy 判平）。派单=D-424。
- 治理：strategy_stats 的 disabled_reason 追加「归因作废」注记；复活与否走业主资金议程，不与修 bug 合票。

## 证据

- check: `grep -rn "Bid *- *[a-zA-Z_.]*Ask" internal --include=*.go | grep -v _test` 列出全部成对公式，逐条比对腿序
- result: 6 处中 5 处 `sell.Bid−buy.Ask`，仅 engine_basis.go:110（currentNetBps）与 :67（currentBasisBps，清算侧语义正确）反写；currentNetBps 消费点比较阈值语义=剩余价差 → 错配
- check: 上述 SQL 持仓时长中位数 vs 扫描节拍（30s 节流/5-10min fallback）
- result: MEANREVERT 中位 9.4 s ≈ 首轮 kick 扫描；收盘时刻等间距=tick 签名
- ref: docs/开平仓管线逻辑审计-2026-09-23-Devin.md §1 P1-1；D-424

## 反证条件

- check: 修正腿序后回放 8-25 五单报价序列（qbus 快照或 fin ticks），看退出是否在真实收敛点触发而非首轮
- falsifies_when: 修正后仍首轮判平（=另有 clip/阈值语义问题），或原 25 单确有 ≥5 单在首轮前价差已实际收敛至 ≤0.5bps

## 适用边界

- 一切「入场定义 X 的对偶退出条件」：入场与退出公式必须同一函数或同一腿序约定；审计手段=同语义公式全库 grep 对齐 + 持仓时长分布对照扫描节拍（中位≈tick 即退出恒真）。
- 不适用于清算侧基差语义（currentBasisBps→basisStopLossCheck 用 basis−anchor 差值，腿序正确），二者名称相近需在注释钉死口径。
