# internal/research/scan

> 事实截至搬迁 HEAD（M2.1，filter-repo 保史）。判定：决策席末节填。

## 职责
扫描器——机会候选枚举（纸面/研究产物，INTRA 已剔）。

## 边界
- **上游**：internal/data/symdig
- **下游**：internal/data/fact, internal/data/keydomain, internal/store/pgstore

## 结构
72 个 go 文件（测试 38）。dNNN_ 前缀文件 0（M2.1 入场门已按关注点改名）。

## 测试
- 测试文件 38；依赖 TestDSN/storetest 的 2
- 超限文件：internal/research/scan/best_pair_test.go:407; internal/research/scan/red_anchor_498_shape_test.go:370; internal/research/scan/scanner_spot_carry_structure_test.go:406; internal/research/scan/r1_kill_priority_test.go:344; internal/research/scan/graduate_matrix_test.go:332; internal/research/scan/carry_universe_test.go:346

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
