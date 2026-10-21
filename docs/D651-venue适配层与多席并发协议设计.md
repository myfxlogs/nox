# D-651 修复设计稿 —— venue 适配器注册层 + 多席并发协议

> 编制依据：D-651 裁定（2026-10-21）。本稿为过 D-251 设计核验的施工蓝本。
> 病灶锚：okx 产品拿 binance 价评估+入账（五处 venue 盲写实证）；/opt/nox 部署树同日三撞+他席未提交面阻断提交+53300 测试库挤兑；STATE 新行五连「未见」。
> 原则：新所接入=加文件不改共享径；并发共享面=有协议有锁，不再裸奔。

## A. venue 适配器注册层（P0·钱径）

### A.1 现状实病

`dciexec` 按 binance 单所建，多所化后成散裸写：`spotPrice`(binance 基址)、`sigmaDaily`(binance 优先兜序)、`usdOf`(binance 价换算)、`disposal_sweep`(binance 现货卖出)、`DCIExecVenues`/`EarnExecVenues` 裸 map。已合 `spotPriceVenue` 集中 switch 是过渡态——新所仍需改共享码。

### A.2 设计

```go
// internal/platform/venuepricer/（新包——归平台域；dciexec 消费不内嵌）
package venuepricer

// Adapter venue 定价面契约。只收「读」面（价/σ）；写面（下单/划转）
// 留各 executor——本包不管 mutating。
type Adapter interface {
    // SpotPrice 现货参考价；交割锚产品用 Index 口径 venue 原生价。
    // base=基币（BTC）；读败→err（调用方 fail-closed）。
    SpotPrice(ctx context.Context, base string) (float64, error)
    // SigmaDaily 日收益 σ（交割锚同径优先 venue 日 K）。
    SigmaDaily(ctx context.Context, base string, days int) (float64, error)
    // SpotSymbol venue 原生符号（BTC-USDT / BTCUSDT / BTC_USDT）。
    SpotSymbol(base string) string
}

var registry = map[string]Factory{} // venue → 工厂（凭据/base 注入）
type Factory func(c Cred) Adapter

// Register 装配点调用——exchange_configs 每有效行注册一适配器。
// 缺 venue 工厂=该 venue 定价面如实缺（fail-closed 非静默回落别所）。
func Register(venue string, f Factory)
func For(venue string, c Cred) (Adapter, error) // 无注册=err
```

每 venue 一个文件：`binance.go`（`/v3/ticker/price` + fapi K 线 σ）、`okx.go`（`index-tickers` idxPx + history-candles）、`gate.go`（`/api/v4/spot/tickers` + K 线）。**符号格式归适配器内**（SpotSymbol），共享径只见 base。

### A.3 消费点迁移

| 现件 | 改向 |
|---|---|
| `dciexec.spotPriceVenue` | 保留薄壳→委托 `venuepricer.For(venue)`；errSpotNoBase 哨兵保留（无注册行=凭证缺口语义复用） |
| `dciexec.sigmaDaily` | 按提案 venue 取该所适配器 σ——**禁 binance 先兜**（okx 产品用 binance σ=跨所波动率口径差）；venue 无 σ 径=fail-closed 如实 |
| `dciexec.usdOf` | 已带 venue 参——内部换注册表消费 |
| `dciexec.disposal_sweep` | 处置卖出按所得所在 venue 路由（现钉 binance=OKX 所得不可达病灶） |
| `DCIExecVenues`/`EarnExecVenues` | 归能力账本 `fin_capability_constraint` 投影（`exec_venue:<tmpl>@<venue>`）——venue 可执行面 DB 驱动 |

### A.4 接线

`cmd/core.buildDCIExecutor`：ListExchangeConfigs 逐行→`venuepricer.Register`。新所接入=写 `venuepricer/<venue>.go`+`init()` 注册——**零改共享径**（开闭原则兑现点）。

### A.5 验收锚

- 机检：三 venue 适配器价/σ 单测（httptest 桩）；`For("kraken")` 无注册=err 锚；`dciexec` 107 测零回归
- SG：okx dci 提案/执行日志价源=okx idxPx（首实证）；binance 提案价源=binance（零回归）
- 对抗：删 `okx.go` 的 `init()` 注册→okx 提案如实拒出凭证缺口（非裸错）

## B. 多席并发协议（P1·运维）

### B.1 现状实病

AGENTS §4 假设单席线性：今日三撞（他席 dciexec 在修未提交+旧 stash+我 commit 被半成品断测拦两次）；STATE 新行五连「未见」（stale 分支分发时延）；pgstore 套 53300 挤兑（多席共用一测试库）。

### B.2 设计

**①部署树写禁强制**：`/opt/nox` 加 `scripts/hooks/check-deploy-tree.sh` 环境闸——`pre-commit` 前置查 `git status --porcelain` 只容 merge/deploy 产生的 `M`（实施席开发须 `git worktree /opt/nox-wt/<票>`）；部署树内检出 `wip/*` 分支=警告。纪律已在契约，本件是**执行器化**（没执行器的纪律=建议）。

**②票文广播件**：`docs/handoff/TICKETS.md` 与 STATE 分离——**追加只写单行票文**（票名/开工指令/STATE 锚），实施席开机 `git pull && 读 TICKETS.md 尾` 而非靠 STATE 行（大表漂移面）；STATE 行保持审计档本位。**禁**把 STATE 拆散（其审计粒度是特性）。

**③测试库逐席隔离**：`pgstore` 测试 `ensureDB` 按 `NOX_TEST_SEAT` env（或 session id）建 `nox_test_<seat>` 库——多席同跑互不占连接池；无 env=共享库现状（不破现有席）。

**④红 CI 冻结窗**（D-652-D① 残余并入）：机检红期间只容修复票并入——复审席口头纪律条，进 AGENTS §4 落文票。

### B.3 验收锚

- 部署树内 `git checkout wip/*`+commit=被闸拦；worktree 径正常
- 两席同跑 pgstore 套零 53300（各库各池）
- 新票文当日实施席零「STATE 未见」报告（D-651 反证锚③）

## C. 边界与禁线

- venue 适配层只管**读面**（价/σ）；mutating（下单/划转/赎回）留各 executor 不进本包——钱径写面无抽象是既有纪律（每 executor 已 venue 分派）
- 端点 path/JSON 字段形=码面属适配器内；在册与否/base_url/凭据/符号格式=DB+能力账本
- σ 源换 venue 原生后数值微变——尾险界随动，属正确换口径非回归
- 并发协议三件互不依赖，可分票施工；A/B 两域可并票（A 钱径优先）

## D. 派序与验收窗

1. **A 票先行**（12-25 交割前 settle 口径必修——#1827 计时弹）
2. B①+B③ 同票（工程面小）；B②+B④ 归 AGENTS 落文票（与 D-652-C 落文票并批，D-251 门后）
3. 首验收窗：下次 okx go 执行（价源日志）+下张 dci 提案卡（APR=实选档 apy 已在产，复验沿用）
