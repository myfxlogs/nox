# internal/portfolio/trxvote

> 事实截至搬迁 HEAD（M2.4，filter-repo 保史）。判定：决策席末节填。

## 职责
TRX 托管资产收益管理——投票/再平衡执行面（账本口径不入 8%，D-066）。业绩口径：TRX 质押/投票/能量=业绩外·自营盘（D-629 业主令——链上质押回报=本金自带收益非系统产出；覆盖率面板 RealMoney/Productive=false 不入总资金/在产/加权年化，行保留可见、APR 落 note；position_daily_income 腿级 TRX 收益不剔=系统持仓腿产出）。

## 边界
- **上游**：无（cmd 装配点待 M2.6）
- **下游**：internal/platform/safego, internal/store/pgstore

## 结构
15 个 go 文件（测试 7）。dNNN_ 前缀文件 0（M2.4 入场门已按关注点改名）。

## 测试
- 测试文件 7
- 无超限非测试文件

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
