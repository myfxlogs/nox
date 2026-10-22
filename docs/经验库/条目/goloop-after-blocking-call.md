---
id: goloop-after-blocking-call
kind: pitfall
risk: high
status: verified
keywords: [GoLoopPlain, safego, RunWS, 阻塞调用, 死代码, goroutine, 序位, universe poll, okx, 采集断供]
scope: [data-collect, concurrency, review-checklist]
summary: 后台件启动调用（GoLoopPlain/go func）顺序写在阻塞主循环（RunWS 类，仅 ctx.Done 返回）之后=永不执行的死代码——D-665 实证 okx univpoll 死代码致 okx 宇宙面 boot 起零供给（产验 max(ts) 定格 boot 刻）；后台件必须先启、阻塞调用最后；产验锚=行在流/max(ts) 新鲜度非「代码存在」
skills: []
---

# GoLoopPlain 置于阻塞调用后=死代码（顺序调用序位审计）

## 病灶
`safego.GoLoopPlain(name, fn)` 本身非阻塞（同步起 goroutine 即返），但它若被顺序写在**阻塞调用之后**则永不执行。D-665 产验实证：`RunWS(ctx, spec)` 为阻塞重连循环（仅 ctx.Done 返回），okx univpoll 的 `GoLoopPlain` 写在其后一行（okx_ws.go:101）→ okx 宇宙 REST 拍从未启动，采集面静默断供直至产验 ticks 实测发现（max ts 定格 boot 刻 13:29:16；对照 binance 199行/113符+gate 432行/117符 同期在流）。

## 识别面
- 「起后台件」调用（GoLoopPlain/go func/起 ticker）与阻塞主循环同函数顺序排列时必核相对序位：后台件**先**启、阻塞调用**最后**。
- 正确先例同文件族：binance_market_ws.go:183、gate_ws.go:92——univpoll 均以独立 GoLoopPlain 先于阻塞 loop 启动。
- 测试盲区：单测直调函数体测不出「goroutine 未启动」——该面仅产验（行在流否/max ts 新鲜度）或集成测试可证。

## 复审检查单
- 新增 goroutine 启动点：grep 其所在函数内的阻塞调用（RunWS/select-for/http.Serve），启动行须在阻塞行**之前**。
- 产验锚：新供给面部署后必查 `max(ts)`/行数在流——「代码存在」≠「goroutine 活着」。
