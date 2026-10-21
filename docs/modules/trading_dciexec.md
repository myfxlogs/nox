# internal/trading/dciexec

> 事实截至搬迁 HEAD（M2.3，filter-repo 保史）。判定：决策席末节填。

## 职责
DCI 双币理财执行器——申购买卖轮（wheel）与结算。

## 边界
- **上游**：无（cmd 装配点待 M2.6）
- **下游**：internal/data/bus, internal/infra/dephealth, internal/risk/gate, internal/store/pgstore

## 结构
15 个 go 文件（测试 4）。dNNN_ 前缀文件 0（M2.3 入场门已按关注点改名）。

## 测试
- 测试文件 4；依赖 TestDSN/storetest 的若干
- 无超限非测试文件

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。
- 10-21 DCI 复活票：okx 腿尘额 qty 病灶史=6463aa249 版无量界闸+尘划转阈值（生产 #27-29 qty 1.15e-9 连落 failed——资金实躺 savings 池、装配面未含）；89f448dbe 量界闸+05983a96d 尘跳+18a7ca8c9 savings 第三源三件已修，TestOkxDCILeg_DustQtyMagnitude 锚钉回退。
- 收益账跳账面四处（settle/okx_settle/disposal/disposal_sweep）cerr/行缺原静默——10-21 补 log 钉（生产 #7/#8 income_events 欠账 2.8028 USDT 在案）。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
