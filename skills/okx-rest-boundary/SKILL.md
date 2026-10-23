---
name: okx-rest-boundary
description: okx REST 界桩账——钦定保留面（事务操作）vs 退役面（轮询）+族闸键谱+h2 感染病灶根修实证
---
# okx-rest-boundary
## 证据
- SG 15min REST 普查基线→退役后：funding 88→0、ticker 26→残存兜底拍、account/balance 44→1、positions 6→1
- 闸件：`RestFamilyOnFn`（`NOX_OKX_REST_<FAMILY>` 族闸默认关 fail-closed）+`OkxPrivRestAllowedFn`（`NOX_OKX_REST_ACCOUNT` 私面单点闸）
- 界桩裁定：「100% 零 REST 不可得」——目标是**实时数据零轮询+事务按需单发**
## 钦定保留面（venue 无 WS 等价=REST 正选型，禁再议退）
| 端点 | 域 |
|---|---|
| `/finance/savings/purchase-redempt`、`/finance/savings/balance`、`lending-rate-summary` | Simple Earn 申购/赎回/持仓/利率（金融产品面无 WS op） |
| `/asset/balances`、`/asset/transfer`、withdrawal/deposit-history | 资金户余额/划转/io 史（funding 户无推送频道） |
| `/public/instruments` | 合约目录冷调（bootstrap） |
| `/trade/order`+`/trade/cancel-order`、account/config·leverage·max-size | 下单/撤单/账户操作（事务单发；exec 钱径走 FreshClient） |
| `/market/ticker` exec 时读 | 下单瞬间价 sanity（事务面）+cexscan 交割/缺 key 兜底拍 |
## 退役面（实时数据 WS 已覆盖——轮询闸关）
- funding-rate(-history) REST ← WS `funding-rate` 频道
- market tickers 轮询 ← WS `tickers` 频道
- account/balance·positions 周期读 ← WS `balance_and_position`+account
## JIT earn 归集（闲钱不眠执行面）
- **闲额混源**：`free = unified〔WS 快照须鲜<5min〕+ funding〔/asset/balances REST〕`——单边读败/快照陈=fail-closed 整拍停，金不动（`okx_event` 行只含交易户，单读=假贫病灶实证复返过一次）。
- **动态保证金留备**（D-业主令最优解，替静态 $500）：`reserve = Σ armed okx 腿组合保证金需`——`margin_usd 直值 > notional_usd/lever > 组合默认额`；零 armed/读败→静态兜底 `NOX_OKX_MARGIN_RESERVE_USD`（500）。键：`NOX_OKX_MARGIN_LEVER`(5)/`NOX_OKX_MARGIN_COMBO_USD`(pct 型组合默认 150)/`NOX_OKX_MARGIN_RESERVE_CAP`(2000 上限)。实现：`pgstore.JitOkxArmedMarginUSD`+`dciexec.jitOkxReserve`。
- **扫额式**：`sweepable = free − reserve − floor(100)`——floor 防碎钱空转/滞回防抖；armed 优先权经留备保证（信号到时交易户当场有钱开单，不等赎回时延）。
- **事件驱动+单飞**：balance_and_position push 钩子触发，`TryLock` 单飞防并发余额事件双过闲额判定。
## 坑注记——h2 感染病灶（根修实证 2026-10-23）
- **签名**：`malformed HTTP response "\x00\x00\x12\x04..."`=h2 SETTINGS 帧落 h1 解析器
- **根因**：`http.DefaultTransport` 一经使用即被懒配 h2（`onceSetNextProtoDefaults`→TLSNextProto 注 handler+`TLSClientConfig.NextProtos` 注 `"h2"`）；`tr.Clone()` **浅继中毒字段**→ALPN 供 h2→服务端回 h2 帧→h1 client 判 malformed。`ForceAttemptHTTP2=false` 只挡懒配时机、不挡已染字段继承。
- **修法**：`assembleRT` clone 后 `NextProtos=[]string{"http/1.1"}`+`TLSNextProto` 空表清零；CA 并入同 tc 防覆写丢钉
- **排查法**：裸 tr vs 装配栈**同进程交错对照**（fresh 5/5 败 vs bare 5/5 通）→二分钉出 httpx 层；DNS/IPv6/中间盒/指纹诸嫌疑逐件否证
