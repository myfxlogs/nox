---
schema_version: 2
id: cross-broker-cfd-basis-risk
kind: pitfall
risk: high
status: observed
keywords: [跨broker基差, CFD对冲, AUDJPY, 报价不同步, 浮亏不对称, ticks滞后, MT5 pnl反推, 周五收盘]
scope: [SWAPARB, CROSS_VENUE, MT5, CFD, 多broker对冲]
summary: 同品种 CFD 跨 broker 报价不同步——收盘流动性下降偏移可达 5+ pips，对冲两腿浮亏不对称（看似对冲实则裸露基差）；ticks 价格滞后 MT5 内部价，估算浮亏失真
first_seen: 2026-08-22
last_verified: 2026-09-22
skills: [事实核验]
evidence_refs:
  - type: server_fact
    source: postgres
    query: SELECT venue, symbol, bid, ask, ts FROM ticks WHERE symbol IN ('AUDJPY','AUDJPYm') AND venue IN ('52993526','277842155') AND ts >= '2026-08-21 18:50:00' ORDER BY ts DESC LIMIT 10
    observed_at: 2026-08-22T01:50:00Z
    server_revision: ""
    response_hash: ""
  - type: server_fact
    source: rpc
    query: ListBrokerPositions
    observed_at: 2026-08-22T01:49:00Z
    server_revision: ""
    response_hash: ""
  - type: commit
    ref: 533a92c
  - type: doc
    ref: docs/handoff/STATE.md:49
---

## 事实/症状

持仓 #264 SWAPARB AUDJPY（BUY 52993526 @113.517 / SELL 277842155 @113.503，各 1 手），周五收盘后业主发现浮亏 $41.47。

- BUY 腿 MT5 pnl = +284.33（swap +4.99）
- SELL 腿 MT5 pnl = -325.80（swap 0）
- 净 pnl = -41.47，净 swap = +4.99，净总 = -36.48

ticks 表最后一条 AUDJPY 报价在 2026-08-21 18:57 UTC（周五外汇收盘），52993526 bid=114.146 / 277842155 ask=114.158——看起来两 broker 价差仅 1.2 pips，对冲良好。但 MT5 实际 pnl 显示的亏损远大于此价差所能解释。

## 根因或策略解释

**跨 broker 基差风险**：同一品种（AUDJPY）在不同 broker（IC Markets vs Exness）的 CFD 报价来自各自的流动性提供商，不完全同步。周五收盘后流动性下降，两 broker 报价出现 5.2 pips 偏移。

从 MT5 pnl 反推隐含当前价格：
- 52993526 隐含 bid = 113.969（ticks 表记录 114.146，偏差 17.7 pips）
- 277842155 隐含 ask = 114.021（ticks 表记录 114.158，偏差 13.7 pips）
- 两 broker 隐含价差 = 5.2 pips

**ticks 表价格滞后**：ticks 采集在 18:57 UTC 停止（外汇收盘），但 MT5 服务器内部价格仍可能更新（周末滚动/隔夜利息调整等），导致 ticks 表价格与 MT5 服务器实际价格出现显著偏差。

浮亏分解：
- 跨 broker 基差（当前价差不对称）：$32.67（79%）
- 开仓价差成本（1.4 pips）：$8.80（21%）

## 处置

- **当前**：不立即平仓，等周一开盘观察两 broker 基差是否回归再决定（业主指示）。
- **排查方法**：当浮亏与 ticks 表价差不符时，用 `ListBrokerPositions` RPC 获取 MT5 实际 pnl，反推隐含价格，对比 ticks 表确认偏差来源。
- **待评估**：① 跨 broker 基差风险是否需要加入 SWAPARB 入场门槛（当前 EntrySpreadMinBps 只看开仓瞬间价差，不看跨 broker 历史基差波动）；② ticks 采集是否需要在周末/收盘后标注 stale 避免误用。

## 证据

- check: `ListBrokerPositions` RPC → BUY pnl=+284.33 swap=+4.99 / SELL pnl=-325.80 swap=0
- check: ticks 表 `WHERE symbol='AUDJPY' AND venue IN ('52993526','277842155') AND ts>='2026-08-21 18:50:00'` → 最后报价 18:57 UTC，bid 114.146 / ask 114.158
- check: MT5 pnl 反推隐含价 = openPrice + pnl × USDJPY / CS → BUY bid 113.969 / SELL ask 114.021
- check: `autotrade_state` → DAILY_LOSS_HALT（日亏 -$100.90 ≤ -$100，8/21 08:47 UTC 触发）
- result: ticks 表价差 1.2 pips vs MT5 隐含价差 5.2 pips，偏差 4 pips；浮亏 $41.47 = 基差 $32.67 + 开仓价差 $8.80
- ref: commit 533a92c, STATE.md:49
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49566
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49567
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49568
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49574
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49575
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49576
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49577
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49578
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49579
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49580
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49586
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49587
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49593
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49599
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49600
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49601
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49602
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49608
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49609
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49610
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49616
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49629
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49630
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49631
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49632
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49634
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49636
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49637
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49638
- 2026-08-28 重现: cross-broker-cfd-basis-risk | ref: alerts#49639
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49640
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49641
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49642
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49643
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49644
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49645
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49646
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49647
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49648
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49649
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49652
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49653
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49654
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49655
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49750
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49751
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49758
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49759
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49760
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49761
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49762
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49763
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49764
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49770
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49771
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49772
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49773
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49774
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49775
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49776
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49777
- 2026-08-29 重现: cross-broker-cfd-basis-risk | ref: alerts#49778
- 2026-08-30 重现: cross-broker-cfd-basis-risk | ref: alerts#50015
- 2026-08-30 重现: cross-broker-cfd-basis-risk | ref: alerts#50016
- 2026-08-30 重现: cross-broker-cfd-basis-risk | ref: alerts#50021
- 2026-08-30 重现: cross-broker-cfd-basis-risk | ref: alerts#50024
- 2026-08-30 重现: cross-broker-cfd-basis-risk | ref: alerts#50027
- 2026-09-18 重现: cross-broker-cfd-basis-risk | ref: alerts#57862
- 2026-09-18 重现: cross-broker-cfd-basis-risk | ref: alerts#57864
- 2026-09-18 重现: cross-broker-cfd-basis-risk | ref: alerts#57865
- 2026-09-18 重现: cross-broker-cfd-basis-risk | ref: alerts#57867
- 2026-09-18 重现: cross-broker-cfd-basis-risk | ref: alerts#57868
- 2026-09-22 重现: cross-broker-cfd-basis-risk | ref: alerts#58568
- 2026-09-22 重现: cross-broker-cfd-basis-risk | ref: alerts#58569
- 2026-09-22 重现: cross-broker-cfd-basis-risk | ref: alerts#58572
- 2026-09-22 重现: cross-broker-cfd-basis-risk | ref: alerts#58573
- 2026-09-23 重现: cross-broker-cfd-basis-risk | ref: alerts#58751
- 2026-09-23 重现: cross-broker-cfd-basis-risk | ref: alerts#58753
- 2026-09-23 重现: cross-broker-cfd-basis-risk | ref: alerts#58755
- 2026-09-23 重现: cross-broker-cfd-basis-risk | ref: alerts#58757
- 2026-09-23 重现: cross-broker-cfd-basis-risk | ref: alerts#58758
- 2026-09-23 重现: cross-broker-cfd-basis-risk | ref: alerts#58759
- 2026-09-23 重现: cross-broker-cfd-basis-risk | ref: alerts#58763
- 2026-09-23 重现: cross-broker-cfd-basis-risk | ref: alerts#58768
- 2026-09-23 重现: cross-broker-cfd-basis-risk | ref: alerts#58771
- 2026-09-23 重现: cross-broker-cfd-basis-risk | ref: alerts#58773
- 2026-09-23 重现: cross-broker-cfd-basis-risk | ref: alerts#58774

## 反证条件

- check: 周一开盘后查 `ListBrokerPositions` 看 #264 两腿 pnl 是否回归对称（净 pnl → 0 附近）
- falsifies_when: 周一开盘后基差完全回归（净 pnl ≈ 0）→ 说明周五收盘后的偏移是流动性真空的暂时现象，非结构性基差风险
- check: 对比周一开盘后 ticks 表 vs MT5 pnl 反推隐含价是否一致
- falsifies_when: ticks 表与 MT5 隐含价一致 → 说明 ticks 滞后只在收盘后发生，盘中数据可信

## 适用边界

- 适用于跨 broker CFD 对冲策略（SWAPARB / CROSS_VENUE）。
- 当前证据仅来自 AUDJPY 一对 broker（52993526 IC Markets / 277842155 Exness），其他 broker 对的基差特征可能不同。
- ticks 表 vs MT5 价格偏差目前只在周五收盘后观察到，盘中是否存在未验证。
- 反推隐含价用 USDJPY = 159.04（收盘前最后报价），周一开盘后 USDJPY 变化会影响反推精度。
