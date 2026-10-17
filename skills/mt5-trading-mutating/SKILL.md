---
name: mt5-trading-mutating
description: mtapi MT5 mutating 面（OrderSend/Modify/Close）——demo 户全谱状态：端点缺口挂请求箱，真户永久禁跑
---

# mt5 mutating 面（2026-10-17：demo 端点缺口——全谱未实测如实缺）

| RPC | 功能 | demo 实测（2026-10-17，BTCUSDm） | 关键字段 |
|---|---|---|---|
| OrderSend | 市价开仓（0.01 BUY）/挂单（BuyLimit 深价） | **ok**：ticket=2701577885（市价）/2701577888（BuyLimit@1000 深价挂） | operation=OrderType_OrderType_* 枚举；volume；price*（pending 必）；sl/tp*；comment* |
| OrderModify | 改 SL/TP（仓）/改价（pending） | **ok**：两态均成功 | ticket+stoploss/takeprofit（仓）；price（pending 改价） |
| OrderClose | 平仓/撤挂 | **ok**：市价仓平+pending 撤 全周期闭环 | ticket（pending 撤同 RPC） |

**连接正径（demo）**：`Service.Search(company="Exness")` → Results[].Access 列表（**host:port 形态须拆分**——首版整串当 host 致 Connect disposed/timeout）→ Connect 凭据=/opt/nox-secrets/mt5-demo.env（MT5_DEMO_USER/SERVER/PASS）。实拍 endpoint=18.163.85.196:443。
**隔离钉（真户永久禁跑）**：ICR/EXR/XMR 三真户 Trading RPC 禁调（真钱）；demo 数据禁写生产表；ChangePassword **自伤型永禁**（改密断访——唯一永未实测项）。
**符号方言硬坑**：demo 户同 Exness 系=BTCUSDm/XAUUSDm 型 m 尾（XAUUSD 裸名 not exist 实拍同 EXR 真户）——**symbol 名一律 venue SymbolList 实读禁猜**；周六仅加密面开（外汇对停盘勿试）。
**复跑**：`go run ./tools/mtprobe --dsn <dsn> --demo-env /opt/nox-secrets/mt5-demo.env --out docs/能力账`。
**请求字段速查**：OrderSend{symbol,operation(OrderType 枚举),volume,price*(pending),slippage,sl,tp,comment}；OrderModify{ticket,stoploss,takeprofit,price*}；OrderClose{ticket,lots*,price*,slippage*}。
