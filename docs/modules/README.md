# docs/modules — 模块页索引

每包一页 ≤100 行，从代码读出，带 `file:行` 出处；新建页复制 `_模板.md`。`<pkg>.零引用.txt` = 该包**包外零引用**的导出符号清单（不等于死码，阶段 3 逐个定性用）。

| 包 | 判定 | 阶段 3 挂钩 |
|---|---|---|
| [store/pgstore](store.md) | 保留 | 37 个零引用方法逐个定性 |
| [adapter](adapter.md) | 保留 | adapter→reconcile 方向（3a 已修） |
| [collect](collect.md) | 保留 | exchange/ 分层倒置（3a 已修） |
| [exec](exec.md) | 保留 | 无 |
| [autotrade](autotrade.md) | 保留 | kill switch 无外部入口先查 |
| [reconcile](reconcile.md) | 保留 | 无 |
| [venue](venue.md) | 保留 | 3a 新立 |
| [dashboard](dashboard.md) | 局部重构 | 60 个监控组件文件迁出，再拆 Service |

未覆盖的 63 个 internal 包（engine/position/settlement/scan/detector/judge/…）按触碰顺序补页，不预先写。
