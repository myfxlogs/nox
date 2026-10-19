# internal/data/collect/exchange

> 事实截至搬迁 HEAD（M2.1，filter-repo 保史）。判定：决策席末节填。

## 职责
CEX WS/REST 采集——binance/okx/gate 行情与私流，写 facts/ticks。

## 边界
- **上游**：—（本票消费者后续 M2.x 挂入）
- **下游**：internal/data/bus, internal/data/fact, internal/data/keydomain, internal/data/venue, internal/infra/dephealth, internal/platform/alerttext, internal/platform/httpx, internal/platform/safego, internal/platform/signutil, internal/store/pgstore

## 结构
87 个 go 文件（测试 40）。dNNN_ 前缀文件 0（M2.1 入场门已按关注点改名）。

## 测试
- 测试文件 40；依赖 TestDSN/storetest 的 1
- 超限文件：internal/data/collect/exchange/gate_subscribe_frame_test.go:611; internal/data/collect/exchange/exchange_history_test.go:598

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |

## io/earn 采集面体检表（earn-io 账面完整性专项 10-19）

| venue | 端点 | 面 | 采集态 |
|---|---|---|---|
| binance | `/sapi/v1/capital/deposit/hisrec` | 充值史 | ✅ 在案（cex_io_fetch direction=deposit） |
| binance | `/sapi/v1/capital/withdraw/history` | 提现史 | ✅ 在案（direction=withdraw，applyTime 串解析坑在案） |
| binance | `/sapi/v1/asset/transfer` | 通用划转史 | ✅ 在案（tranId 键；**枚举不含 earn 钱包对**=划转盲区根因） |
| binance | `/sapi/v1/simple-earn/flexible/history/subscriptionRecord` | 申购史 | ✅ 在案（`se-<purchaseId>` 键） |
| binance | `/sapi/v1/simple-earn/flexible/history/redemptionRecord` | 赎回史 | ✅ 在案（`se-<redeemId>` 键） |
| binance | isolated-margin 四型 transfer | 逐 symbol 划转 | ◌ **不可采域**（需逐 symbol 参数——域未启用如实标） |
| binance | `simple-earn/locked/*`（定期） | 定期申赎 | ◌ **漏端点**——locked 面未采（系统在册仅 flexible 产品，残差=如实缺） |
| okx | `/api/v5/asset/deposit-history` | 充值史 | ✅ 在案 |
| okx | `/api/v5/asset/withdrawal-history` | 提现史 | ✅ 在案 |
| okx | `/api/v5/asset/bills` | 资金账户账单（划转） | ✅ 在案（billId 键；from/to 账户码映射） |
| okx | savings purchase/redempt 史 | earn 申赎史 | ◌ **漏端点**——`/api/v5/finance/savings/*` 史端点未采（系统侧 exec 写径记账补位=残差解释项） |
| gate | 划转/earn 史 | — | ◌ **不可采域**——gate 无独立 io 采集件（cex_io_fetch case 表只 binance/okx；gate earn 走 rail 写侧记账） |

**dedup 键域对齐（本专项落点）**：执行侧 earn 申购/赎回 external_id 改采 `se-<purchaseId>`/`se-<redeemId>`（回执抽，回执无键=落提案/产品 id 兜底域）——采集器史回放同键撞 `ON CONFLICT DO NOTHING` 幂等不双记（断言 TestVenueIOEarnDualWriteDedup 钉死）；读侧 `SumVenueIOTransfers` 剔除面扩 `se-% AND to_wallet='earn'`（申购行订单腿已覆盖——双计防堤随键域迁）。jit-*/rot-*/earn-red:* 兜底域不回撞=残差如实标。
