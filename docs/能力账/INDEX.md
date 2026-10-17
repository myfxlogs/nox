# 能力账索引（D-608——venue 端点全图登记）

| 账 | 件 | 覆盖 |
|---|---|---|
| binance 端点清单 | `binance_endpoints.gen.txt` | 三源并集 773 端点（postman=597/md=341/conn=552/union=773/mutating=205） |
| binance probe 证据 | `bn-api-probe-2026-10-17.pb` | 568 端点实测（ok=62/400=27/404=153/401=241/418=84/500=1——签名分档+param-catalogue+凭据 fail-closed 后真值域：401=凭据权限/IP 白名单实拍非空跑；400 余=真须参族 BTCUSDT 喂参后残余 27；418 封禁 pace 在岗仍触发） |
