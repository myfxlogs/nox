---
schema_version: 2
id: pxbt-swaptype0-override-inpoints-4x-deviation
kind: pitfall
risk: high
status: verified
keywords: [PXBT, 1314615, SwapType0, InPoints, override, swap归一化, D-104, 规格值vs实际结算, TickValue=0, D-092, guardrail]
scope: [adapter, mt_swap, swapledger, SWAPARB, D-104, D-112, PXBT, broker_accounts]
summary: D-104 P1 T1 把 PXBT SwapType=0+非零 rates override 为 InPoints 模式，但 PXBT 实际结算公式与标准 InPoints 差 4 倍——归一化 bps=-0.2929，实际结算折算 bps=-1.17，导致 #293 SWAPARB 预期 +$0.015/d 实际 -$0.17/d 方向反转
first_seen: 2026-08-26
last_verified: 2026-09-26
skills: [事实核验, KB维护, 生产取证]
evidence_refs:
  - type: server_fact
    source: cmd/probe_pxbt_swap
    note: "PXBT GBPJPY SwapType=0 SwapShort=-6.3508 Points=0.001 TickValue=0 CS=100000；IC SwapType=1 SwapShort=-22.900 同模式归一化准确（偏差1x）"
  - type: server_fact
    source: broker_positions RPC
    note: "#293 PXBT SELL GBPJPY swap=-$0.17（11h），swapledger 记账 -$0.04/d，偏差 4.3x"
  - type: code
    source: internal/adapter/mt_swap_mode.go:101-105
    note: "swapTypeZeroAsInPoints map['1314615']=true——D-104 P1 T1 override（2026-09-26 复核：venue 1314615 账号行 9-24 已删，map 条目成死配置但无害，留作 D-493 删除残留样本）"
  - type: code
    source: internal/adapter/mt_swap.go:51
    note: "InPoints 归一化: bps = s * point / price * 10000——假设 swap=s*point*qty*cs/USDJPY，但 PXBT TickValue=0 内部用不同 tick_value 结算"
---

## 症状

SWAPARB 持仓 #293（PXBT SELL GBPJPY + IC BUY GBPJPY）预期净 swap +0.2328 bps/d（+$0.015/d），实际每日亏损 -$0.17/d（PXBT 腿），方向反转。持有 11h 累计亏 $0.10，平仓止损 realized_pnl=-$0.26。

## 根因

D-104 P1 T1 override 把 PXBT SwapType=0+非零 rates 当 InPoints 处理（`s * point / price * 10000`），但 PXBT 实际结算公式与标准 InPoints 差 4 倍。

**三 venue 对比实证**（2026-08-26 probe）：

| venue | SwapType | SwapShort 原始 | 归一化 bps | 实际结算/d | 偏差 |
|-------|----------|----------------|------------|------------|------|
| PXBT (1314615) | 0→override | -6.3508 | -0.2929 | -$0.17 → -1.17 bps | **4.0x** |
| IC (52993526) | 1 (标准) | -22.900 | -1.054 | -$0.07 → -1.05 bps | 1.0x ✅ |
| FxPro (REDACTED_MT_ACCT) | 1 (标准) | -36.400 | -1.676 | — | — |
| CMC (39050818) | 1 (标准) | 7.693 (long) | 0.6749 | +$0.04 → 0.67 bps | 1.0x ✅ |

**关键**：IC/FxPro/CMC 的 SwapType=1（标准 InPoints）归一化准确（偏差 1x），只有 PXBT SwapType=0 override 偏差 4 倍。PXBT 的 TickValue=0（API 未返回），MT5 内部用非零 tick_value 结算，导致归一化公式假设的 `swap = s * point * qty * cs / USDJPY` 与实际 `swap = s * tick_value_internal * qty` 不匹配。

## 修复

1. **立即止损**：平仓 #293（realized_pnl=-$0.26，停止每日 $0.17 出血）
2. **D-112 立项**：PXBT swap 归一化修复——SwapType=0 override 需重新标定，或改用 PerLotCurrency 模式，或从 MT5 DealHistory 反推实际 tick_value
3. **D-110 P2 对账告警**：明日首轮对账应检出 PXBT 4 倍偏差（偏差率 76% > 10% 阈值 → critical）

## 教训

1. **SwapType=0+非零值 override 需运行时验证**：D-104 P1 T1 override 基于静态归一化假设，未经实际结算验证。override 应附"运行时对账验证"门禁——首笔持仓结算后核对规格值 vs 实际值，偏差 >50% 自动告警 + 暂停该 venue 开仓。
2. **TickValue=0 是危险信号**：InPoints 归一化依赖 `swap = s * point * qty * cs / fx`，但 MT5 内部可能用 TickValue（非 point*cs/fx）结算。TickValue=0 意味着 API 未返回内部结算参数，归一化公式假设可能不成立。
3. **规格值 ≠ 实际结算**：MT5 规格参数（SymbolParams）是静态声明，实际结算用 MT5 内部 tick_value（可能动态变化）。跨 broker 对比验证是唯一可靠标定方法——单 venue 静态归一化无法发现 4 倍偏差。
4. **D-092 guardrail 阈值过宽**：`swapInPointsGuardUSD=500`（每手日 swap $500 才告警），PXBT GBPJPY 实际每手日 swap ~$17（0.01 手 $0.17），远低于阈值 → guardrail 未触发。guardrail 应增加"跨 broker 同符号归一化值对比"维度，而非只看绝对值。
