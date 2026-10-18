# TRADIFI_PERPETUAL 族评实据（S2桥假设#5 立项票——评估段零真金全 GET）

## ① 目录实据（binance fapi exchangeInfo，SG 出口实拍 10-18）
- `contractType=TRADIFI_PERPETUAL` 品种 **221 个**，`underlyingSubType` 分布：TradFi=221、Pre-IPO=4（子类型可复合）
- **计价面切分**：quoteAsset/marginAsset=**USD1 的仅 1 件=SPCXUSD1**（假设#5 的 shape=USD1 命中即此件）；余 220 件全 USDT 计价——USD1 面=单一品种试验田
- SPCXUSD1 规格：baseAsset=SPCX（标普类指数底层）、underlyingType=EQUITY、quantityPrecision=2/pricePrecision=5、onboardDate=1784538000000（2026-07 上板）
- premiumIndex 实拍：markPrice=143.38/indexPrice=143.96、lastFundingRate=0、interestRate=0、fundingInfo cap/floor=±2%——收益通道=资金费率/基差（现值近零），**无申赎**——generic_yield 族确装不进
- 同类 USDT 面示例：XAUUSDT/XAGUSDT（金属）、TSLAUSDT/NVDAUSDT（个股）、SPYUSDT/QQQUSDT（ETF）、CLUSDT/NATGASUSDT（大宗）——对腿映射面富
## ② 对腿映射（MT5 broker_symbol_map 实拍——底层同根）
| binance 底层 | MT5 对腿（三户在册） |
|---|---|
| SPCX（股指） | SPX500：US500(15032313)/US500m(3001157)/US500Cash#(110660304) |
| US30 系 | US30/US30m/US30Cash# 三户均在 |
| XAU/XAG（USDT 面） | XAUUSD*/XAGUSD 系（方言名 venue SymbolList 实读） |
| 个股/ETF | MT5 个股 CFD 面按户而异——逐 symbol 实读禁猜 |
## ③ 族归裁定（草案——决策席落 D# 裁）
**裁定建议=「crypto perp × MT5 CFD」xasset 对冲变体**（cfd_swap_hedged 族扩 instType）：标的本质与 MT5 股指/金属 CFD 同根；funding-only 单腿否决（lastFundingRate=0/interestRate=0——单腿无 carry 源）。memo 风险疑点三记：休市日历/USD1 脱锚/样本薄（1 行）——采集轴（资金费率/标记价/休市历）未建前不可评，**试单入册须决策席另闸**。
## ④ 参数面沉点（本票 diff）
`s2bridge.go` 新增 TRADIFI_PERPETUAL 路由：腿参数=params_proto{mt_venue,mt_symbol,side,cex_symbol} 显式供给→复用 s2LegCombo→xasset_allow_combos 白名单；腿缺=fail-closed dedup warn「拒猜腿位」。**LLM memo 只顾问——腿参入册属决策席供给面**。
