---
name: mt5-trading-mutating
description: mtapi MT5 mutating 面（OrderSend/Modify/Close）——demo 户全谱状态：端点缺口挂请求箱，真户永久禁跑
---

# mt5 mutating 面（2026-10-17：demo 端点缺口——全谱未实测如实缺）

| RPC | 功能 | 实测状态 | 阻塞点 |
|---|---|---|---|
| OrderSend | 开仓/挂单（OrderType 枚举含 BuyLimit 等 pending 型） | **未实测** | demo 端点缺口 |
| OrderModify | 改 SL/TP/pending 价 | **未实测** | 同上 |
| OrderClose | 平仓/撤挂 | **未实测** | 同上 |

**隔离钉（真户永久禁跑）**：ICR/EXR/XMR 三真户 Trading RPC 禁调（真钱）；demo=Exness-MT5Trial5/277842155（业主供凭据 /opt/nox-secrets/mt5-demo.env 600 仓外）。
**端点缺口实拍**：Search(Exness) 解析地址超时+在册三真户端点以 demo 凭据 Connect=INVALID_ACCOUNT（证据 JSON Connect(demo) 行）——供应商按户配发端点面实锤；**请求箱已挂**（fin_condition_gap template_id=mtapi-demo-endpoint kind=credential）。
**复跑**（端点到位后）：`go run ./tools/mtprobe --dsn <dsn> --demo-env /opt/nox-secrets/mt5-demo.env --out docs/能力账`——harness 自动 OrderSend 0.01→Modify SL/TP→Close+pending 挂/改/撤全周期。
**请求字段速查**：OrderSend{symbol,operation(OrderType 枚举),volume,price*(pending),slippage,sl,tp,comment}；OrderModify{ticket,stoploss,takeprofit,price*}；OrderClose{ticket,lots*,price*,slippage*}。
