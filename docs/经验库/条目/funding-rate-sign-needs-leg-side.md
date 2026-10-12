---
schema_version: 2
id: funding-rate-sign-needs-leg-side
kind: pitfall
risk: high
status: verified
keywords: [funding rate, 资金费符号, 腿方向, leg side, swapledger, position_cashflows, 错号]
scope: [swapledger, ledger_entries, funding, CEX perp, XASSET_CARRY]
summary: 资金费收入符号必须乘腿方向（多付空收）——无符号公式隐含假设 funding 腿恒空，首个多 perp 收负费率仓把收息记成付息
first_seen: 2026-09-17
last_verified: 2026-09-17
skills: [事实核验]
evidence_refs:
  - type: decision
    ref: D-322
  - type: doc
    ref: internal/swapledger/ledger_entries.go
  - type: server_fact
    source: cex
    query: GET /api/v4/futures/usdt/account_book?contract=CL_USDT&type=fund
    observed_at: 2026-09-17T09:00:00Z
---

## 事实/症状

pos 485（XASSET_CARRY：gate CL_USDT 多 + MT WTI 空）持仓页 swap 列显示 −0.2，但实际资金费是**收**不是付——gate account_book 流水实证两笔正入账（+0.1209/+0.0973）。

## 根因或策略解释

`legSwapAmount` funding 分支 `amount = rate/100/365 × notional` 无腿方向符号。资金费方向规则：**rate>0 多头付空头；rate<0 空头付多头**。无符号公式只对「空 perp」正确——系统史上 funding 腿全是空 perp（PERP_FUNDING 形态），潜伏到首个「多 perp 收负费率」仓（XASSET_CARRY D-320）才踩出。另：venue 实报真值（gate pnl_fund）被账本估算无条件覆写是放大器。

## 处置

D-322：funding 分支 `leg.Side=="BUY"` 取负；显示层 venue 真值优先（bp.Swap≠0 不覆写不双计）。**裁决手段：直接拉 venue 账户资金流水（gate account_book type=fund / binance userTrades funding 行）看 change 正负，勿信自家账本推导**。

## 证据

- check: gate testnet account_book type=fund
- result: change=+0.1209125（收）vs 账本 estimated −0.1964（付）——异号实证
- ref: D-322；merge=4d52a764

## 反证条件

- check: 任一 BUY perp 腿在 rate<0 时 cashflow 为负
- falsifies_when: 出现该形态则符号规则仍有缺口

## 适用边界

CEX perp funding 记账与显示；MT swap 走 swap_long/swap_short 事实已带腿向不受此坑。历史 legacy 行（quality=NULL）BUY 腿 21 行同错号（未污染统计，旧结算不并入 cashflow）。
