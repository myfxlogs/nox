---
name: okx-market-ws
description: okx 公开行情 WS 面能力账——books5/tickers/funding-rate/mark-price/index-tickers/instruments 全谱实测与订阅坑
---
# okx-market-ws
## 证据
- 实现件：`internal/data/collect/exchange/okx_ws.go`、`okx_ws_books5.go`
- SG 产验（2026-10-23）：syms=7、books5 全谱订阅活、182-271 行/2-3min 落库
- 退役面实证：funding-rate(-history) REST 轮询清零后 WS 频道持续供数（`facts` 表 `src=okx_ws_v5` `kind=funding` 行流）
## 订阅面（公共频道全谱）
- `books5`：深度五档——原生 instId 键域（如 `BTC-USDT-SWAP`），节流+异步落库（读路径禁同步 DB 写）
- `tickers`：last/bid/ask/vol —— cexscan 价差读 `LatestTickMidWithin(60s)` 吃此面事实
- `funding-rate`：资金费率推送——REST `/public/funding-rate(-history)` 轮询已闸关退役（`NOX_OKX_REST_FUNDING` 豁免键备）
- `mark-price`/`index-tickers`/`instruments`：标记价/指数/合约目录推送
## 坑注记
- **无效符号单点拒不误伤**：`SILVER-USDT-SWAP` 等 venue 不存在合约被单符拒绝、其余符号照流——目录脏符不阻断面（但仍应清目录）。
- **ack≠存活**：订阅 ack 收讫不代表帧在流——存活判据=帧时戳前移（同 binance 族教训）。
- **REST depth 兜底闸关**：`NOX_DEPTH_REST_OKX=1` 才开，默认 fail-closed（缺数据≠降级数据——宁缺交易不静默降级）。
- **WS 即 h1 升级**：okx WS 是 HTTP/1.1 Upgrade（RFC6455 非 h2/h3）——WS 通≠h2 通证；REST 传输病灶须单测（见 `docs/经验库/条目/` h2 感染条目）。
