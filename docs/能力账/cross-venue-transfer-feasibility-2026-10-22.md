# 跨所划转轨可行性取证（D-662——MT5 USD $5.2k→CEX 息轨通路）

> 性质=可行性取证非执行票；真金划转零动。取径：SG 生产 broker_accounts 实读+venue 官方文档/帮助中心对拍+skills 能力账对拍。

## ① 三户实盘 broker 面（SG 实读）

| venue | broker | server | is_real |
|---|---|---|---|
| 110660304 | XM（XMR…）| XMGlobal-MT5 2 | true |
| 15032313 | IC Markets | ICMarketsSC-MT5-6 | true |
| 3001157 | Exness | Exness-MT5Real | true |

**mtapi 出金 API 面=无**：三 broker 出金全走客户专区人工操作（MT API 无出金端点——系统不可自动触发；轨顶=「提案→业主执行→入金观测」半自动径）。

## ② Broker 出金通道对拍（官方/帮助中心面）

| broker | crypto 出金 | 约束 |
|---|---|---|
| XM | 有（USDT/BTC/ETH/XRP 等 27 种，免手续费，~24h 内处理） | **利润段才可 crypto 出金**；且须①本人名下钱包②有 crypto 入金履历③先入金方法全额出清后利润段才可走 |
| IC Markets | 三方源称 USDT(ERC20/TRC20) 出金在（官方面以「入金方法同径返还」为总则——Bitcoin 入金→bankwire 出金 20 AUD 费在案） | 入金方法未用 crypto 则 USDT 出金大概率不开放（待客服实录核） |
| Exness | 官方页只见「本人名下账户」总则+支付方式优先级；crypto 出金未在官方文档明示（PA 实面待查） | payment method priority rules |

**入金端（CEX）面**：binance/okx 充值地址生成本就是常规 API——但 bn-sapi mutating 面全族黑名单未实测（能力账如实缺，非不支持）；CEX 侧 USDT(TRC20) 入金免费、分钟级确认。

## ③ 成本模型（USD $5,247 全量转移假设径通）

| 项 | 估算 | 依据 |
|---|---|---|
| broker 出金费 | $0（XM crypto 免费/ICM crypto 免费，bankwire 20 AUD 备选） | 官方面 |
| 链上费 | ~$1×3 笔（USDT-TRC20） | 链常态 |
| 时延机会成本 | 出金审批 ~24h + 链上 ~分钟 ≈ 1 天息 | 假设 CEX 活期 ~2.5%：$5.2k×2.5%/365≈$0.36 |
| 合计 | **< $5** | — |
| 对标收益 | CEX 承接轨 ~2.5-5.9% ann ≈ **$130-310/yr**（相对零息现状） | okx earn 5.9%/binance 2% 实产在案 |

**回本=即时**（成本<$5 vs 年息 $130+——经济面明确通过，约束全在通道存在性）。

## ④ 裁定面（径在/径缺）

- **径半在**：CEX 入金端无障碍；broker 出金端=crypto 出金通道存在但全受「入金同径/本人钱包/利润段」约束——**条件依赖业主侧入金履历与钱包实名**（非系统面可裁）。
- **非 API 自动轨**：出金触发=客户专区人工（业主动作）——系统侧可建=划转提案轨（金额+目标 CEX+入金地址+观测对账），非执行轨。
- **续建票条件**：业主答入金履历面（三户各自入金方法）→XM 若 crypto 入金在案=crypto 出金即通（最小阻力径）。

## ⑤ 业主条件请求（fin_condition_gap 候选）

per-broker 入金方法履历取证（XM/ICM/Exness 各自入金走卡/电汇/crypto——决定出金可选径）+本人 crypto 钱包/CEX 同名账户确认。
