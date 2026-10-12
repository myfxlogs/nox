---
schema_version: 2
id: funding-annualization-remaining-time-bug
kind: pitfall
risk: high
status: verified
keywords: [funding rate, 年化, 剩余时间, 结算间隔, periodsPerDay, 虚高, 假机会, OKX, Binance, Gate, fundingTime, nextFundingTime, next_settle_time]
scope: [collect/exchange, FUNDING_HEDGE, detector, evaluator, 全策略]
summary: fundingPeriodsPerDay 用「剩余时间」当结算间隔 → 结算前极短 → 年化虚高 8.7 倍假机会入库；修复=用「nextFunding-prevFunding」结算间隔
first_seen: 2026-08-22
last_verified: 2026-09-22
skills: [事实核验]
evidence_refs:
  - type: server_fact
    source: okx_api
    query: GET /api/v5/public/funding-rate?instId=BTC-USDT-SWAP
    observed_at: 2026-08-22T02:38:00Z
    server_revision: ""
    response_hash: ""
  - type: server_fact
    source: postgres
    query: SELECT count(*) FROM opportunities WHERE type='FUNDING_HEDGE' AND gross_bps > 50
    observed_at: 2026-08-22T02:42:00Z
    server_revision: ""
    response_hash: ""
  - type: commit
    ref: "D-079 commit"
  - type: doc
    ref: internal/collect/exchange/exchange.go:26-41
---

## 事实/症状

业主引用机会 #657569（FUNDING_HEDGE BTCUSDT okx:spot→binance:perp），gross_bps=94.9%，net_bps=90.4%，质疑是否真实。

DB 实证：25 条 FUNDING_HEDGE 机会 gross_bps=94.9%（真实应 10.95%），facts 表 12 条 funding rate value=94.9%。

## 根因或策略解释

`fundingPeriodsPerDay` 函数用**剩余时间**（nextFunding - now）当结算间隔：

```go
// 旧 bug
periodMs := nextFundingMs - nowMs  // 剩余时间，随当前时刻变化！
periodsPerDay := 86400000 / periodMs
```

每次接近结算时刻（每 8h），剩余时间从 8h 缩短到几分钟，periodsPerDay 从 3 飙升到 26+，年化从 10.95% 爆炸到 94.9%。

OKX 还有第二个 bug：代码读了 `fundingTime`（当前结算时刻）而非 `nextFundingTime`（下次结算时刻），导致剩余时间更短（fundingTime < nextFundingTime）。

**正确做法**：用「nextFunding - prevFunding」（结算间隔），与当前时刻无关。OKX API 同时返回 `fundingTime` 和 `nextFundingTime`，差值即间隔。Binance/Gate 不返回上次结算时刻 → 固定回退 3/天（8h）。

## 处置

1. **fundingPeriodsPerDay**：参数从「nextFundingMs（剩余时间）」改为「intervalMs（结算间隔）」，不再依赖 `time.Now()`。
2. **OKX**：用 `nextFundingTime - fundingTime` 算间隔（两个字段 API 都返回）。
3. **Binance/Gate**：API 不返回上次结算时刻 → 固定回退 3/天（8h）。
4. **DB 清理**：DELETE 25 条假机会（gross_bps>50）+ 12 条假 facts + VACUUM。
5. **回归测试**：TestFundingPeriodsPerDay_D079Regression 锚定 8h 间隔 → 3/天 → 10.95% 年化。

## 证据

- check: OKX API 返回 fundingTime=00:00 UTC, nextFundingTime=08:00 UTC → 间隔 8h → 3/天 → 10.95%
- check: 旧代码 fundingTime-now=54.4min → 26/天 → 94.9%（虚高 8.7 倍）
- check: DB 25 条 FUNDING_HEDGE gross_bps=94.9% → 全部是 bug 产物
- check: 修复后 fundingPeriodsPerDay(8*3600*1000)=3, annualizeFunding(0.0001,3)=10.95%
- result: DELETE 25 rows + 12 facts + VACUUM
- ref: D-079, exchange.go:26-41

## 反证条件

- check: 部署后新入库 FUNDING_HEDGE 机会 gross_bps 应 ≤ ~15%（0.01%×3×365×100=10.95%，极端 funding rate 0.03% 时 32.85%）
- falsifies_when: 新入库 FUNDING_HEDGE gross_bps > 50% → bug 未修或 API 返回新字段格式变化
- check: facts 表 funding value 应 ≤ ~33%（funding rate 上限 0.03%×3×365×100）——**限主流币种口径**；carry_universe 扩全量 perp 后小币种真极值常态化（9-22 实测 max=430.99 %/yr，ALCH/B2/PHAROS 类 0.4%/8h 脉冲为真费率非 bug）
- falsifies_when: 主流币（BTC/ETH 等）facts value > 50 或同 symbol 年化随「距结算剩余时间」日内漂移 → fundingPeriodsPerDay 仍用剩余时间

## 适用边界

- OKX 可精确算间隔（API 返回两个字段）；Binance/Gate 只能回退固定 3/天。
- 如果未来 Binance/Gate API 开始返回上次结算时刻，可升级为精确间隔。
- funding rate 本身随市场波动，年化值会变化，但不应因「距结算的剩余时间」而变化。
- 此 bug 只影响年化展示和机会检测，不影响实际结算金额（结算按真实 funding rate × 仓位）。
