---
schema_version: 2
id: gate-spot-tickers-poison-pair-batch-kill
kind: pitfall
risk: high
status: verified
keywords: [gate, spot.tickers, 订阅, payload, 静默拒绝, 非法 pair, WS 零帧, 数据看门狗, subscribe batch]
scope: [arb-core, collect/exchange, gate_ws, gate.io v4 ws]
summary: gate spot.tickers 订阅 payload 中任一非法 pair 静默拒整批（无错误帧零数据）；futures.tickers 同批容忍——增量订阅可长期掩盖，全量重连一次团灭
first_seen: 2026-09-28
last_verified: 2026-09-28
skills: [事实核验]
evidence_refs:
  - type: doc
    ref: docs/施工-D-537-gate-spot订阅合法对白名单-施工提示词-2026-09-28-Devin.md
---

## 现象

gate spot WS 连接活着（握手/订阅回显正常）但零数据帧，mt-health 报 `gate quote stale`、数据看门狗 10min 杀→重连→同死循环；futures 连接同订阅集却数据如常。

## 根因或策略解释

gate v4 `spot.tickers` subscribe 语义：**payload 数组中只要含一个该平台不存在的 pair，整个订阅被静默拒绝**——无 error 帧、无部分生效，直接一条 update 都不推。`futures.tickers` 对同批垃圾对容忍（无效对跳过、有效对照推）。

隐蔽点：watchlist/符号集是**增量生长**的——历史合法对增量批一旦订上就持续推流，后来混进的非法对增量批被拒只是无声少数据；**只有全量重连时整集进一帧才一次团灭**。症状「重启后断流，重启前一直好」即此形态。

## 处置

- 订阅边界过官方合法对白名单（`GET /api/v4/spot/currency_pairs` / `futures/usdt/contracts`），滤后入 payload；白名单 REST 失败回退不过滤
- 不依赖上游报错做检测——拒批是无声的，健康信号=数据帧到达率（数据看门狗已兜住识别侧）
- 订阅集含跨 venue 拼接对（`symbol+"_USDT"`）时按目标 venue 过滤是必做不是可选

## 证据

- check: 同链路探针（cex.myfxlogs.org + X-Proxy-Token）分别订 `[BTC_USDT]` 与生产全集 74 对
- result: 单对 9 帧/15s；全集 spot 仅回显帧零数据 vs futures 630 帧/20s；分 chunk 二分定位毒对=商品/指数名（COPPER/NAS100/JP225 等）+中文符号（牛来/龙虾），纯合法 chunk 65 帧/10s 正常
- ref: D-537 提示词 §1 证据表；生产日志 wsrunner data watchdog kill 两连

## 反证条件

- check: 滤后白名单重连
- falsifies_when: 若合法对白名单订阅仍零帧 → 问题在连接/上游而非 payload 毒对

## 适用边界

- gate.io v4 WS 订阅面（spot.tickers 严格；futures.tickers 宽松——两端语义不对称，勿以一侧行为外推另一侧）
- 其他 venue 未实证同语义，排查 WS「连活零帧」时应把「payload 含非法项」列入嫌疑单
