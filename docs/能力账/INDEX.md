# 能力账索引（D-608——venue 端点全图登记）

| 账 | 件 | 覆盖 |
|---|---|---|
| binance 端点清单 | `binance_endpoints.gen.txt` | 三源并集 773 端点（postman=597/md=341/conn=552/union=773/mutating=205） |
| binance probe 证据 | `bn-api-probe-<date>-o<N>.pb` | 批化登记——每批≤150 端点+5min 冷却+ping 预检+418 即停；文件携 offset 不互盖（注记㊷：同日文件名覆盖伤销账）；批 0 局部件 `bn-api-probe-2026-10-17-o0.pb`（40 端点 abort 封禁记忆钉） |
