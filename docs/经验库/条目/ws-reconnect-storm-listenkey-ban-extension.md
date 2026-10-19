---
schema_version: 2
id: ws-reconnect-storm-listenkey-ban-extension
kind: pitfall
risk: high
status: verified
keywords: [binance, listenKey, -1003, 封禁, 重连风暴, 盲锤, 续期, 退避, flap, user ws, 资金路径]
scope: [internal/collect/exchange, binance user ws, venue 用户流重连设计, cex-proxy]
summary: 通道 flap×重连盲锤 listenKey→-1003 封禁且锤会续期封禁（实证）；修=封禁窗感知退避（窗内零 REST，到期+抖动恢复）。
first_seen: 2026-09-30
last_verified: 2026-09-30
skills: [KB维护]
evidence_refs:
  - type: commit
    ref: 378f6284
  - type: doc
    ref: internal/collect/exchange/binance_user_ws_reconnect.go
---

## 事实/症状

cex-proxy 通道 flap 风暴期间（连败/恢复 3-4min 交替 40min+），binance 用户流每次重连 POST 新 listenKey（5s 退避）→ POST /fapi/v1/listenKey 频率超限 -1003 封禁出口 IP（ws-perp-user 面判定劣化告警×3：11:54/11:57/12:02 BJ）；封禁窗内 5s 盲锤延续，第二次告警显示封禁到期从 04:10:50 UTC 推移至 04:14:51（+4m01s）——盲锤续期了封禁。

## 根因或策略解释

①重连循环与 listenKey POST 同生命周期：flap 放大 POST 频率（每次 runOnce 先 POST）；②错误分支不看错误类型——-1003 封禁响应与普通断线同等待遇（5s 后再 POST），封禁窗内发射既浪费又可能续期封禁；③「-1003 期间锤只是失败无害」的直觉错误：限频响应携带新到期时戳，持续命中会刷新窗口。

## 处置

Run 循环遇 -1003/418 解析封禁到期时戳（`banned until <epoch ms>` 正则复用 alerttext.bannedRe 导出）→ 退避至到期+0..30s 随机抖动，窗内零 REST 发射；短封禁按到期节拍重试（非 max(5s)，到期即试）；常规错误 5s 节奏不变。封禁诱因本身（flap）另票治本。

## 证据

- check: alerts 表 03:50-04:12 UTC 三条「封禁出口IP至 …（错误码 -1003）」到期时戳对比+TestD591 两测（窗内零 POST/到期即恢复，盲 5s 变体对抗双红 2/2 次）
- result: 修复后封禁窗内零 listenKey POST、到期+jitter 单次恢复尝试
- ref: commit 378f6284（D-591 S1）；S0 归因=flap 风暴×盲锤（premiumIndex 418 已于 11:30 D-582 退役，11:5x 新 -1003 与 REST 轮询无关）

## 反证条件

- check: 若 binance 对封禁窗内命中不刷新到期时戳（固定首封时长）
- falsifies_when: 封禁窗内持续 POST 而到期时戳不再推移

## 适用边界

适用「重连即重新鉴权/取凭证」的 venue 用户流（binance listenKey 形态；okx/gate 私有流 WS 直连无 REST 凭证面不在此列）。泛化：**限频封禁窗内任何自动重试都必须先读封禁到期时戳再定节拍**——退避基准是「交易所说的解封时刻」不是本地固定间隔。同族：httpx 通用退避不在本票（venue 特化路径先改）。
