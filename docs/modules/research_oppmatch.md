# internal/research/oppmatch

> 事实截至搬迁 HEAD（M2.4，filter-repo 保史）。判定：决策席末节填。

## 职责
提案匹配——模板×账本周期匹配产出 pending 提案（fin_proposal）。

## 边界
- **上游**：无（cmd 装配点待 M2.6）
- **下游**：internal/risk/gate, internal/store/pgstore

## 结构
5 个 go 文件（测试 2）。dNNN_ 前缀文件 0（M2.4 入场门已按关注点改名）。

## 测试
- 测试文件 2
- 无超限非测试文件

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。
- 10-21 D-643 步①：eval 卡词源轨标注（live/replay/point-query/prior 四值钉死，evidence 串带（track=X）+evidence_refs tracks 摘要面零迁移）+串轨 warn 闸（point-query 源喂 live-轨语义→InsertAlertDedup warn 24h 侦测立账不拒——拒级归 D-633 开口）。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
