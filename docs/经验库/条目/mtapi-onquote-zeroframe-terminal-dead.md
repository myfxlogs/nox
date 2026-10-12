---
schema_version: 2
id: mtapi-onquote-zeroframe-terminal-dead
kind: pitfall
risk: high
status: verified
keywords: [OnQuote, 零帧, zeroframe, stream 活着无数据, 心跳空帧, mtapi, mt5, 报价停摆, grpcurl 探针, terminal 侧死, ICR15032313, ic52993526]
scope: [internal/adapter, mt5, mtapi 网关, MT5 终端/VPS, gRPC server-streaming 消费方]
summary: OnQuote 流建连/订阅成功且不断流（Recv 无 error）但只吐 {} 心跳空帧、零真实报价帧——重连机制够不着的失效形态（它只认 Recv error）；根因在 MTapi 网关/MT5 终端侧市场数据推送死，客户端无锅；一元 RPC（swap/margin/trade_mode facts）照常鲜活，venue「看似活着」隐蔽同型
first_seen: 2026-09-22
last_verified: 2026-09-22
skills: [事实核验, 生产取证]
evidence_refs:
  - type: server_fact
    source: rpc
    query: grpcurl Connect→Subscribe→OnQuote 60-70s 探针（15032313 仅 {} 心跳帧；3001157 对照组秒级回放量真实帧）
    observed_at: 2026-09-22T00:40:00Z
    response_hash: ""
  - type: server_fact
    source: postgres
    query: SELECT max(ts) FROM ticks WHERE venue IN ('15032313','52993526') -- 停于 9-18 20:59 / 9-19 10:01 UTC
    observed_at: 2026-09-22T00:30:00Z
    response_hash: ""
  - type: doc
    ref: docs/经验库/条目/grpc-server-stream-reconnect-on-break.md
---

## 事实/症状

- mt-health：`ICR15032313`、`ic52993526` 自 boot 起 `q:n/a`（从未产一帧报价），其余 11 个 MT venue 全 `q:0s`；swap/margin/trade_mode 三路 facts 照常产（~30min 新鲜）。
- ticks 分区：15032313 停于 2026-09-18 20:59 UTC、52993526 停于 2026-09-19 10:01 UTC——曾经产报价，停 3-4 天，跨重启未愈。
- grpcurl 实拍（决定性）：对 15032313 执行 Connect→Subscribe→OnQuote，前两步全 OK，OnQuote 流开 70s 只收 `{}` 心跳空帧、零真实报价帧；对照组 3001157 同法探 30s 秒级回放量真实帧。

## 根因或策略解释

`grpc-server-stream-reconnect-on-break` 覆盖的是「Recv 返回 error→流断」形态；本条是**流无错而零帧**——网关把流建起来、心跳照发，但终端侧 Market Watch/市场数据推送死了，没有 error 可触发重连，客户端重连机制完全够不着。一元 RPC 采集路径与推送流是两条通道，故 facts 鲜活与报价死可同时成立（同 `heartbeat-quote-fools-freshness-gate` 的「时间戳新≠可交易」同型隐蔽）。

## 处置

- 诊断配方（先分清客户端 vs 终端侧，勿盲改客户端）：grpcurl 对 MTapi 网关 `Connect`→`Subscribe`→`OnQuote` 开流计时——**有真实帧=客户端侧问题**（查重连/订阅集）；**只有 {} 心跳=网关/终端侧死**（业主侧查 VPS MT5 终端 Market Watch、重启终端/MTapi）。对照组取一健康 venue 同法探，验证探针方法本身。
- 缓解：当前唯一手段=业主侧重启终端/网关，或 `DisableBroker` 止噪（venue 无 OPEN 腿无敞口时安全，5208379 先例）。
- 预防（**已建检测层**，D-432 已部署 2026-09-24）：zeroframe watchdog 双侧同构（MT4/MT5）——心跳活+真帧死>90s→stage-1 去重 warn+stage-2 单发取消流强制重开（不重建 session）；重连后>60s 仍死→persist warn「终端侧死重连不治本」；流静止（心跳也停，含周末休市）不动作归 Recv error 路径；真帧恢复→episode 清零记 recovered。缩短暴露时间≠治本，业主侧重启终端仍是根治手段。

## 证据

- check: grpcurl Connect→Subscribe→OnQuote @15032313 开流 70s
- result: Connect/Subscribe 全 OK；70s 内仅 `{}` 心跳空帧，零真实报价帧
- check: 同法 @3001157 开流 30s（对照组）
- result: 秒级回放量真实报价帧——探针方法有效，差异在 venue 侧
- check: `SELECT max(ts) FROM ticks WHERE venue IN ('15032313','52993526')` + facts 表新鲜度
- result: ticks 停 9-18/9-19；trade_mode/swap/margin facts 当日 00:30 UTC 仍在产
- ref: 决策席 boot 分诊 2026-09-22（待归纳两行 quote critical 升真信号呈报业主）

## 反证条件

- check: 业主重启 MT5 终端/MTapi 网关后再探 OnQuote
- falsifies_when: 若重启后仍零帧且对照组正常 → 网关侧该 venue 配置/订阅路由死而非终端推送死，探针结论细化但「客户端无锅」不变；若零帧流伴随 Recv error → 退回 `grpc-server-stream-reconnect-on-break` 覆盖形态

## 适用边界

- 判据「只有 {} 心跳=终端侧死」的前提是 Subscribe 已确认成功；Subscribe 失败本身属另一类（查订阅集镜像/权限）。
- 探针区分粒度=「客户端 vs 网关+终端整体」，不再细分网关与终端——细分须业主侧看 MT5 终端 Market Watch 是否自行更新。
- MTapi/MT5 专属运维面，不晋升 ~/kb；「一元 RPC 鲜活≠推送流健康」的隐蔽故障模式已是他条目同型注记。
