---
schema_version: 2
id: gate-api-shape-must-match-sdk
kind: pitfall
risk: high
status: verified
keywords: [gate, gateapi-go, API形状, SDK对拍, mock失真, futures订单, positions, finish_as, funding字段, 转述失真]
scope: [exec, gate, cex, perp, demo]
summary: gate API 形状以官方 SDK 模型+docs 逐字为准——mock 与提示词转述两轮同源出错：id string/status canceled/positions 数组/funding 字段均与官方事实不符
first_seen: 2026-08-24
last_verified: 2026-10-07
skills: [KB维护, 事实核验]
evidence_refs:
  - type: decision
    ref: D-098 返修包①复审通过+返修②
  - type: commit
    ref: cbd3b31
  - type: doc
    ref: internal/exec/cex_executor_gate.go:44
  - type: server_fact
    source: cex
    query: GET https://api.gateio.ws/api/v4/futures/usdt/contracts/BTC_USDT（主网公开端点，字段名实测全对）
    observed_at: 2026-08-24
  - type: server_fact
    source: cex
    query: GET https://api.gateio.ws/api/v4/futures/usdt/order_book?contract=XAU_USDT —— asks/bids=[{p:价,s:张数}] 对象数组（非字符串对），contracts 端点 quanto_multiplier=0.0001
    observed_at: 2026-10-07
---

## 事实/症状

gate 接入两轮施工栽在同源缺陷——「形状按提示词/别家转述，未对拍 gate 官方事实」：

1. **R1 三处**（b8da48c→f183289）：① 订单 id 按 string 解析，官方 SDK FuturesOrder.Id=**int64**（JSON number，unmarshal 必炸）；② 判 status `canceled/rejected`，官方 Status 只 `open/finished` 两值（死代码），取消/IOC 未成交原因在 `finish_as`——零成交 IOC 落 fallthrough 返 0-qty 假 Fill；③ `positions/{contract}` 按数组解析，官方 GetPosition 返**单 Position 对象**（ListPositions 才数组）。
2. **R2**（cbd3b31 复审新发现）：positions 响应解析 `funding` json 字段——官方 SDK 全版本（Go master + Python 4.47.0→7.1.7 逐版查证）+ API 文档示例**均无此字段**（funding 费用在 `pnl_fund`=Realized PNL-Funding Fees、`realised_pnl` 含结算）；疑为 okx positions 同构转述。探针阶段二「结算真实」判定结构性失效（恒 ⚠️ 未变化）。
3. **R3**（fe3b42c 复审新发现）：account_book 解析 `Time int64 json:"time"` vs 官方模型 Time=**float64** + 真实响应样本逐字 `"time": 1682294400.123456`（带小数）→ unmarshal int64 必炸；`RefID json:"refid"` vs 官方/真实响应=**trade_id**（+id，无 refid）。
4. **R4**（D-112，2026-10-07）：`/futures/usdt/order_book` 按 binance/okx 同构的 `[][]string` 字符串对解析——实拍返回 `{p:价,s:张数}` 对象数组，json unmarshal 全败使 gate 深度自 D-420 起**全灭**（fin_cex_depth gate 行=0）；且 `s`=合约**张数**非 base 量（须 × contracts 端点 quanto_multiplier，XAU_USDT=0.0001）。单测 stub 也按同一错误假设写=假绿——**stub 复刻假设不证真实形状**。

## 根因或策略解释

- mock 只能证明「代码与 mock 一致」，不能证明「与真实 API 一致」——mock 形状=施工时点的主观假设，随提示词转述失真（尤其跨交易所同构想象：okx 有 funding 字段≠gate 有）。
- 类型也必须对拍：官方模型 Time=float64（带小数 unix 秒）而凭直觉写 int64=真实响应必炸；同一字段名跨 SDK 版本/真实响应漂移（refid→trade_id）需以真实响应样本为准。
- 官方 SDK 由交易所 OpenAPI 规范生成，是形状事实的权威源；docs 字段表逐字同样权威。未经逐字段对拍就施工 = 在假设上编码。
- 服务端运行时响应=最终真相（server-facts-over-docs），但 SDK/文档对拍是**运行前**的最低成本防线。

## 处置

- gate 系适配器形状事实一律以官方 SDK 模型源码（`model_*.go` / `docs/*.md` 字段表）逐字段对拍为准，别家交易所同构仅作假设不作事实。
- 已落实（cbd3b31）：Id int64+strconv.FormatInt、status 只 open/finished+finish_as 三态判定、positions 单对象解析。
- 探针加 M4② 同款原始响应 body 打印（≤500B）作一次性形状实证——mock 证明不了真实形状时直接打真实响应。
- 返修②（待施工）：探针阶段二证据通道双保险（account_book?type=fund 真实结算行 + positions 原始 body 打印），一次运行定案 funding 字段是否存在。

## 证据

- check: gh api repos/gateio/gateapi-go/contents/model_futures_order.go + model_position.go + api_futures.go + docs/FuturesOrder.md、FuturesApi.md、Position.md、FuturesAccountBook.md；python SDK 历史版本逐版；web 真实响应样本
- result: Id=int64；Status 两值；finish_as 九值逐字；GetPosition→Position 单对象；全版本无 `funding` 字段；FuturesAccountBook.Time=float64（样本 1682294400.123456 带小数）+字段 trade_id/id 无 refid
- ref: D-098 返修包①复审 + cbd3b31 + f183289 + b8da48c

## 反证条件

- check: testnet（fx-api-testnet.gateio.ws）恢复后跑探针打印 positions/account_book 原始响应 body
- falsifies_when: 真实响应含 `funding` 字段（服务端超集）→ 仅 R2 funding 子事实推翻，M1-M3 结论不变；真实响应 time 为整数 → 仅 R3 Time 子事实推翻；真实响应出现 string id 或数组 positions → 主体结论推翻

## 适用边界

- gate futures API v4（USDT 本位）；其他交易所（binance/okx）需各自对拍自身官方 SDK——okx positions 确有 funding 字段，同构想象方向相反照样错。
- R3 修复建议：Time 改 float64（对拍官方模型）；RefID 改 TradeID json:"trade_id" 或删字段（探针不消费）；fundingEvidence 补 err 打印。
