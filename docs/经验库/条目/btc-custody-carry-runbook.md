---
schema_version: 2
id: btc-custody-carry-runbook
kind: ops
risk: medium
status: draft
keywords: [BTC, 托管仓, funding carry, U本位对冲, LDBTC, Simple Earn, 活期理财, 股东套现, 撤仓铁律, D-171]
scope: [binance, 手动仓位, 托管资产, funding 套利]
summary: BTC 托管仓下蛋操作手册——现货留活期理财不动，U 本位等额空 perp 收 funding（~4.9%/年），BTC 数量全程守恒；铁律=BTC 数量与空仓名义始终相等，股东套现同分钟平对应空仓
first_seen: 2026-10-11
last_verified: 2026-10-11
skills: [生产取证, 事实核验]
evidence_refs:
  - type: decision
    ref: D-171
  - type: server_fact
    source: postgres
    query: SELECT payload FROM cex_private_events WHERE venue='binance' AND channel='account' ORDER BY received_at DESC LIMIT 1
    observed_at: 2026-10-11T00:55:00Z
    server_revision: SG pg:15433
    response_hash: LDBTC f=0.05668170（≈$4.7k @ BTC 82924），现货钱包 BTC=0
  - type: server_fact
    source: postgres
    query: SELECT value FROM facts WHERE kind='funding' AND venue='binance' AND symbol='BTCUSDT' ORDER BY ts DESC
    observed_at: 2026-10-11
    server_revision: SG pg:15433
    response_hash: 90 天 funding 均值 ≈ +4.5%/年（区间 [-4%,+11%]）
---

## 背景与适用

股东挖矿所得 BTC（托管）趴币安活期理财（LDBTC≈0.4%/年）。业主令：收益归保管人（保费性质），**本金（BTC 数量）不亏是硬线**，股东随时可要求套现。

**为何 U 本位而非币本位（COIN-M）**：币本位 1x 空只保 USD 价值——币价翻倍时空仓亏损吃掉一半 BTC 数量，股东按币要回即穿仓。**本金=BTC 数量 → 只能 U 本位对冲**。

## 开仓操作（径 A：有 USDT 保证金，推荐）

1. LDBTC **不动**——继续吃活期 ~0.4%。
2. U 本位合约钱包转入 **~$5,000 USDT**（≈全名义，抗暴涨）。
3. **BTCUSDT 永续 → 开空（SHORT）0.0567 BTC**（名义≈$4,700=持仓等额），杠杆 1x。
4. funding 每 8h（00/08/16 UTC）正时自动到账 USDT。
5. 合计 ≈ 活期 0.4% + funding ~4.5% ≈ **~4.9%/年 ≈ $230**（USDT 结算）。

首次建议先开一半（0.028 BTC 名义）跑一周看 funding 实收再补满。

## 股东套现撤仓标准动作（分钟级）

1. LDBTC 赎回 → 现货 BTC（活期即时）。
2. **同一分钟内**平掉对应空仓（买入平仓）。
3. 现货卖出 USDT → 付股东。
4. 部分套现：卖多少 BTC 减多少空仓，比例跟随。
5. funding 只在整点结算时刻持仓才收/付——平在窗间无罚金。

## 铁律与风控

- **BTC 数量 = 空仓名义，始终相等**——任何一边动了另一边 60s 内跟上；留裸空仓=投机非下蛋。
- **方向别开反**：确认 SHORT 且数量=持仓等量（开成多=加倍裸敞口）。
- **funding 翻负**那几天你付（90 天最低 -4%/年≈日 -0.01%）——连续翻负→平仓等回正。
- **保证金线**：BTC +50% 时补 USDT 或减仓（全名义保证金下 +~90% 触强平线，别等）。
- 无 USDT 备用径：联合保证金模式（Multi-Assets，BTC 作保证金 ~5% 折价）空同名义——BTC 不离开币本位。

## 验证

开仓后核：空仓名义=持仓 BTC×现价（±滑点容差）；首个 00/08/16 UTC 结算点后查 USDT 入账；本金账=BTC 数量恒=0.0567±funding 无关变动。
