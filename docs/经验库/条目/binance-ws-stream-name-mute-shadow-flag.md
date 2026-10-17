---
schema_version: 2
id: binance-ws-stream-name-mute-shadow-flag
kind: pitfall
risk: critical
status: verified
keywords: [ws-dead, markPrice, !ticker@arr, 零帧, ack 后零帧, 影子封禁, shadow mute, binance, fstream, 影子旗标]
scope: [venue-ws, binance, SG, egress-ip]
summary: REST 封禁后 WS 升级形态——按流名定向静默哑火：握手 101+订阅 ack 全成功但 markPrice/ticker 族零业务帧，bookTicker 独活；干净探针可复现=venue 侧旗标非码病
first_seen: 2026-10-17
last_verified: 2026-10-18
skills: [生产取证]
evidence_refs:
  - type: server_fact
    ref: 'SG 零依赖探针 2026-10-17 23:35-23:38Z：SUBSCRIBE !bookTicker→ack+2243 帧/20s 活；SUBSCRIBE !markPrice@arr@1s→ack+0 帧；SUBSCRIBE !ticker@arr→ack+0 帧；btcusdt@markPrice（SUBSCRIBE+raw 路径两方言）→0 帧'
    verified: 2026-10-18
  - type: server_fact
    ref: docker logs nox-core wsrunner.go:228 markPrice+ticker 每 ~10min "no business data frames 10m"→watchdog 杀连→TCP 重连成功→仍零帧（12h 循环，ws-dead 告警 13:35/17:18/23:21Z）
    verified: 2026-10-18
  - type: doc
    ref: docs/handoff/STATE.md 决策注记（binance 流哑定谳 10-18）
related: [probe-shares-prod-egress-ip-ban]
---

## 事实/症状

- `binance-mkt-ws:perp:markPrice` 与 `perp:ticker` 两通道进入「看门狗 10min 杀连→TCP/握手/订阅 ack 全成功→仍零业务帧→再杀」无限环，ws-dead 告警「候选全零帧 21 轮冷却」。
- 同端点同 IP 上 `!bookTicker` 聚合流正常（20s 2243 帧实测）、spot bookTicker 正常、REST ping 200、facts funding/vol24h 由 rest-fallback 覆盖鲜活。
- 干净探针（零依赖 Go WS 客户端，与生产码零共享）同 IP 复现同症状 ⇒ **venue 对该出口 IP 的流名级定向静默旗标**，非订阅帧错/非码病/非连接层故障。
- 时序：REST -1003 封禁 10:57→11:57Z 解除；bnprobe 批跑 13:30-13:48Z；ws-dead 13:35Z 起=封禁余波下 WS 面被补挂影子旗标（REST 明封与 WS 哑火可并存不同步）。

## 根因或策略解释

- binance 上游存在「**静默旗标**」形态：不明文 418/1008 拒，握手与订阅 ack 照回，但指定流名（markPrice 族、24hrTicker 族）永不产帧。出口 IP 被 REST 封禁过后，WS 面按流名定向哑火为常见余波（与 D-531/D-535 记录的「上游按连接 epoch 概率性裁断逐符号订阅」同族——杀招粒度从连接级升到 IP×流名级）。
- 对系统语义：D-109 rest-fallback 是为此设计——dephealth 判流红即低频 REST 补观测 facts，业务面降级但不断粮；看门狗杀连重连保留=自动复绿哨兵+正确告警信号。
- **判别范式**：「ack 成功+零帧」≠订阅失败——干净探针同 IP 复现即可把锅从码面摘到 venue 面；对照活流（bookTicker）+对照死流（markPrice/ticker）即定审查粒度（流名级非连接级非 IP 全封）。

## 修法/防再踩

1. 遇到 ws-dead「候选全零帧」先看**有没有同端点活流**——有活流+死流 ack 正常=venue 影子旗标，码面不背锅，不折腾重连参数/订阅格式。
2. **探测/跑数票（bn-capability 类）解禁窗判据除 REST ping 200 外，须加 WS 面健康信号**（markPrice 流族复帧或 dephealth 绿）——影子旗标在挂时加压 REST 批跑=再封/延期风险。
3. rest-fallback 兜底域（D-109）是此形态的正确承接：保持低频 REST 覆盖，等 venue 解标自然复绿；禁为「修流」乱改订阅参数——参数没变、venue 哑了。
4. 看门狗 churn（10min 杀连循环）成本低且自动探测复绿，保留；其告警即 un-mute 观测锚。
