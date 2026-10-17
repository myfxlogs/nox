# binance 端点能力账（D-608——官方 OpenAPI 机械枚举）

> 来源：https://github.com/binance/binance-api-swagger（官方 OpenAPI 3.0 spec；
> spot api + UM 合约 fapi + 钱包/earn sapi 三系）。**禁凭仓内已用端点当全量**。

## 域分组

| 域 | 端点族 | 权重头 |
|---|---|---|
| spot-account | `GET /api/v3/account`, `GET /api/v3/myTrades`, `GET /api/v3/openOrders` | X-MBX-USED-WEIGHT |
| market | `GET /api/v3/exchangeInfo`, `GET /api/v3/ticker/24hr`, `GET /api/v3/ticker/price`, `GET /api/v3/depth`, `GET /api/v3/aggTrades` | 公开无签 |
| fapi-account | `GET /fapi/v2/account`, `GET /fapi/v2/balance`, `GET /fapi/v2/positionRisk` | X-MBX-USED-WEIGHT-1M |
| fapi-market | `GET /fapi/v1/exchangeInfo`, `GET /fapi/v1/premiumIndex`, `GET /fapi/v1/fundingRate`, `GET /fapi/v1/ticker/24hr` | 公开无签 |
| sapi-wallet-earn | `GET /sapi/v1/capital/config/getall`, `GET /sapi/v1/capital/deposit/hisrec`, `GET /sapi/v1/capital/withdraw/history`, `GET /sapi/v1/asset/transfer`（已踩坑：17 枚举不含 earn 对——D-596 earn 划转盲区根因）, `GET /sapi/v1/simple-earn/flexible/list`, `GET /sapi/v1/simple-earn/flexible/history/redemptionRecord`（已踩坑：earn history 端点 404 死于 /sapi/v1/earn/flexible/… 错前缀） | X-MBX-USED-WEIGHT-1M |
| userdata-ws | `POST /api/v3/userDataStream`（listenKey 流前置）、`wss://stream.binance.com:9443/ws/<listenKey>` | 流面 |
