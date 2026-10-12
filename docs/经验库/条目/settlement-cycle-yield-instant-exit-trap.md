---
schema_version: 2
id: settlement-cycle-yield-instant-exit-trap
kind: pitfall
risk: high
status: verified
keywords: [funding, 结算周期, 瞬时费率, 早退, 平仓条件, 套息策略, 收益源, 零参与, PERP_FUNDING]
scope: [套息/收租类策略平仓规则设计, funding/swap/dividend 等结算周期收益]
summary: 收益按结算周期（如 funding 8h）累积的策略，若平仓判定用**瞬时费率/读数**（如「当前净年化≤0 即平」），费率噪声会在开仓后几分钟触发系统性早退——收益源零参与，每笔只剩开平摩擦。修法=退出判定用「跨过结算窗口后的前瞻差/风险线」。
first_seen: 2026-08-28
last_verified: 2026-08-28
repeat: 2
skills: [KB维护]
evidence_refs:
  - type: decision
    ref: D-129
  - type: server_fact
    source: postgres
    query: SELECT * FROM position_cashflows WHERE position_id BETWEEN 384 AND 389
    observed_at: 2026-08-28T05:00:00Z
    server_revision: ""
    response_hash: ""
---

## 事实/症状

PERP_FUNDING #384~389 六笔全亏（-$0.01~-3.0），持仓 7-10 分钟，funding 现金流=**0 行**——策略论点（跨所 funding 差收租）从未参与任何一笔。开仓时净差 2-6% 年化，几分钟后即被平仓规则杀掉。

## 根因或策略解释

`engine_position.go:41` 平仓条件「当前净 funding 年化 ≤0 → 平」：funding 是 8 小时结算一次的现金流，判定却用瞬时费率；demo/实盘费率均围绕零点高频抖动，瞬时读数破零概率在开仓后几分钟内极高。系统性早退 = 只付成本不收收益。

## 处置

套息策略退出判定的正确形态：①持有必须跨 ≥1 个结算窗口（收益 accrued 后）；②退出信号用**前瞻**差（下一结算窗口的预期收益率 ≤0）而非当前瞬时值；③风险线（基差/保证金）独立于收益判定常驻。开仓侧配对预算：入场价差成本 < 预期 N 窗口收益。

## 证据

- check: #384~389 cashflows 零行 + 持仓时长 7-10 分钟（从未跨 8h 结算点）+ #384 恰 10:00.0 平（10min fallback 轮询首查即杀）
- check: 二次实证（同日 09:33:52）——#378 跨 1 窗持有 8.6h 净 +$0.41（funding +0.114 + 价差 +0.296）后被同规则 `net funding 0.00% <= 0.0%` 平掉：同策略同市场，唯一变量=持有时长，正/负收益分野完整闭环
- ref: D-129 终审（decisions.md）

## 反证条件

- check: 若改为跨窗口持有后仍系统性亏损且现金流已记 funding
- falsifies_when: 收益源已参与仍亏 → 问题在策略论点/成本模型，本条缺陷不是根因

## 适用边界

一切「收益按周期结算」的策略（funding/swap/dividend/债券票息）；价差收敛型策略（收益=价差即时变化）不适用本条。
