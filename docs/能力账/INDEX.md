# 能力账索引（D-608——venue 端点全图登记）

| 账 | 件 | 覆盖 |
|---|---|---|
| binance 端点清单 | `binance_endpoints.gen.txt` | 三源并集 773 端点（postman=597/md=341/conn=552/union=773/mutating=205） |
| binance probe 证据 | `bn-api-probe-2026-10-17.pb` | 568 端点实测（tier 分档后：ok=186/400=137/404=153/401=6/403=1/418=82/500=3——signed 档真发（tier=signed 597 行/postman header X-MBX-APIKEY+query.signature 判据）；401=6 真权限域；400=137=多参自伤残余（ticker/avgPrice 族缺 symbol 判据未中）；404=153 真死；418=82 封禁 pace 在岗仍触发） |
