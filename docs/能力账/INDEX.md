# 能力账索引（D-608——venue 端点全图登记）

| 账 | 件 | 覆盖 |
|---|---|---|
| binance 端点清单 | `binance_endpoints.gen.txt` | 三源并集 773 端点（postman=597/md=341/conn=552/union=773/mutating=205） |
| binance probe 证据 | `bn-api-probe-2026-10-17-o0.pb`/`o150.pb` | 批化 4 批×150+5min 冷却跑毕——`o0`=59 端点（19 ok/23 400/17 404 封禁记忆钉 abort）；`o150`=abort 零行（208B——封禁窗内零覆盖如实钉）；`bn-api-probe-2026-10-17.pb`=批 2/3 同名覆盖伤实证（offset 命名件批 2/3 未带=文件名窗残差在案）；累计覆盖≈137 端点非全谱——禁记忆封禁窗内 repeat 同型钉 |
