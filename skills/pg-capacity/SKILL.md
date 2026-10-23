---
name: pg-capacity
description: nox PG 体积治理能力账——表体账/压缩与保留三板斧/死空间回收/快照降采样（SG 11GB 基线实证 2026-10-23）
---
# pg-capacity
## 证据
- SG 基线（2026-10-23 实拍）：库 **11GB**；构成= `facts` 超表 ~6.8GB（9.0M 行/5 周）+`fin_cex_dci_quote` 864MB（3.9M 行/10 天）+`ticks` ~1.3GB+`fin_venue_account_snapshot` 251MB+`fin_cex_earn_apr` 261MB+`cex_private_events` 179MB+`ticks_2026_10` 560MB 死空间
- 超表配置实拍：`facts` retention=1yr、`ticks` retention=72h——**两表 compression_enabled 均=f**
- 增速锚：facts ~1.3GB/周→年化 ~70GB；dci_quote **~87MB/日无保留≈30GB/年=最大漏面**

## 表体账（决策座：谁的体积买谁的决策价值）
| 表 | 体积 | 保留策略 | 决策价值 |
|---|---|---|---|
| `facts`（超表） | 6.8GB | 1yr（不动——评估面合法需求） | 资金费/行情事实面，评估窗 |
| `fin_cex_dci_quote` | 864MB | **无=漏面** | 已毕业/已死 option 报价零价值 |
| `ticks`（超表） | 1.3GB | 72h 自限 | 近实时价差 |
| `fin_venue_account_snapshot` | 251MB | 无 | 5s 粒明细 7d 后零价值 |
| `ticks_2026_10` | 560MB | — | **0 活行纯死空间** |

## 三板斧（钦定施工序——STATE 票锚）
1. **开压缩**：两超表 `ALTER TABLE <t> SET (timescaledb.compress, compress_segmentby='venue', compress_orderby='ts DESC')`+`add_compression_policy(<t>, INTERVAL '2 days')`——时序压缩常规 10-20×，facts 6.8GB→<1GB；compress_after=48h（查询热点近数据）。**压缩前先锚测近窗查询无回退**。
2. **dci_quote 挂保留**：30-90d 滚动 DELETE 策略或转超表+`add_retention_policy`——折断 30GB/年曲线。
3. **死空间回收**：`ticks_2026_10`（0 活行）`DROP TABLE` 或 `VACUUM FULL`；facts chunks 死元组 550k+在堆。
4. **快照降采样**：`fin_venue_account_snapshot` 连续聚合——7d 内原始 5s 粒，老数据折小时粒。

## 坑注记
- **体积≠性能**：压缩后老 chunk 查询走解压路径——近窗查询锚测先行，评估面读旧数据须接受解压开销（换体积）。
- **retention vs 归档**：drop_after 是物理删——若未来要长窗回测，先归档再删；当前裁定=不建归档层（需求未证）。
- **死元组非死表**：`n_dead_tup` 高=autovacuum 追不上写压，非数据错——VACUUM 件与压缩件分步验。
