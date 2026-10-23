# okx 活期+earn 双向 JIT 回腿设计案（设计验证闸件——复审照准前不施工）

## 范围与依赖
- **前置依赖**：okx 私面 WS 化落地（`okx_private_ws_events.go` `balance_and_position` 频道已分派+audit 落 `cex_private_events`——事件源面在场）+exec 真钱加固件并入（单发传输）。
- **收单方能力**：okx `okxEarnRail`（savings purchase-redemption 双端点，earn_rail.go:64 在场）——白名单/阈值进能力账（`fin_capability` 或 app_config 键域，实施期钉）。
- **参照模式**：bn-sapi-wallet-earn 归集模式（flexible earn subscribe/redeem 幂等+io 记账）——okx 面同构。

## 事件源清单（纯事件驱动零定时器）
| 事件源 | 触发面 | 消费径 |
|---|---|---|
| `balance_and_position` 推送 | 成交结算/外部入金/赎回到账/费用扣划（一切余额变动） | 既有 `onAudit` 落库点挂 JIT 归集判定钩（事件即判定拍） |
| WS 重连 | 断事件窗恢复——重连即事件（非轮询） | OnConnect 钩：重连后取最新快照补跑一次判定 |
| 进程重启 | 冷启动断窗 | boot 装配处跑一次判定（同重连语义） |
| 平仓 settle 事件 | `JitReturnForPosition` 既有钩面扩展 | 结算释放资金→earn 归集判（双向：持仓释放→收单） |

**禁**：定时轮询件一律不造（cron/GoLoop ticker 禁——纯事件驱动钦定）。

## 归集判定逻辑
1. **读现额**：balance_and_position 最新快照（`availBal` 活期户——okx unified 口径）。
2. **阈值闸**：`idle = availBal − minReserve`（armed 组合保证金面优先——armed 缺资面优先权高于 earn 占用=JIT 赎回链预留位，判定前查 armed 缺资）。
3. **收单方评**：候选 earn 产品过评估闸（APR/锁期/风险）——不合格如实报闲（`fin_condition_gap` 或 idle 报告域），禁造不合格单。
4. **归集执行**：`okxEarnRail.Subscribe` 入灵活 earn——幂等键 `jit-earn-okx` 前缀+io 记账（bn 模式同构）。
5. **回腿**：armed 缺资短差 → `JitSweepFn` 既有机制赎回 earn 补资（双向：闲入/缺出同核件）。

## 失败重试语义
| 失败域 | 处置 | 重试 |
|---|---|---|
| 快照读败 | fail-closed 不判定（读败≠闲钱） | 下一事件自然重判（WS 持续事件=自愈窗） |
| 收单方评估拒 | 如实报闲不造单 | 下一事件重评（条件变则过） |
| Subscribe 败 | log+alert 留痕不吞 | 下一事件重试（幂等键防双写） |
| WS 断线 | 重连即事件补判 | OnConnect 钩一次 |
| armed 缺资竞争 | armed 优先——earn 判停不扫 | JitSweepFn 赎回链补资 |

## 机检锚
- 归集判定锚测：快照缺/收单方拒/闲额<阈/合格过闸四态断言
- 事件驱动零定时器断言：代码面零 `time.NewTicker`/`cron` 新增
- armed 优先锚：armed 缺资时 earn 归集不触发断言
