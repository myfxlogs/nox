---
name: bn-userdata-ws
description: binance 用户数据流面能力账（D-608）——listenKey 流+WS 端点族实测
---
# bn-userdata-ws
## 证据
- WS 侧：`docs/能力账/bn-api-probe-2026-10-18-*.pb` 覆盖 `/api/v3/userDataStream`/`/fapi/v1/listenKey` 端点名（listenKey POST=流前置合法件——流面订阅窗测件另议）
- 生产流面证据：`docker logs nox-core`——`binance:ws-perp-user`/`ws-spot-user` 键 dephealth 域在册（10-19 实拍 lastEventUnix 帧戳动）
## 实测能力面
- `POST /api/v3/userDataStream`（listenKey 发）/`PUT`（keepalive 续期）/`DELETE`——**全族已实测活**（user-ws 装配在产——私有流连接就绪面 dephealth MarkFrameOK 喂入）
- **WS 侧流族**（substream 方言）：`outboundAccountPosition`/`balanceUpdate`/`executionReport`（spot）`ACCOUNT_UPDATE`/`ORDER_TRADE_UPDATE`（perp）——生产订阅在案活
## 坑注记
- **ack≠存活铁律**（10-18 实锤）：订阅回 ack{result:null}≠推帧——存活判据=帧戳前移（dephealth MarkFrameOK `binance:ws-*` 键），ack 只证握手可达。
- **venue 裁断形态**：按连接 epoch 概率性裁断（D-535）+聚合流名族停推（10-18 `!markPrice@arr` 族）——`use of closed network connection` 强断重连循环=既有看门狗径。
- **user-ws 无订阅帧**：listenKey+连接建立即就绪（Mark(ok) 位=连接非帧——键判读口径异市场流）。
- **ping goroutine SendRaw 败裸退**洞修在案（@75f7f562d）——写径死必 conn.Close 走重连，保活/看门狗分域。
- **能力探测频控注记**（注记#74②）：probe 批跑须预留生产权重余量——预检 ping=存活验证≠频限合规（418 封禁窗内 ping 仍 200 实证）；批跑限速窗（pace ≥500ms）+错峰生产高负载窗+418 即停 abort 禁续扫——烧穿共享出口 IP 权重=REST 兜底全灭形态（10-19 实证 37 次/4min）。
