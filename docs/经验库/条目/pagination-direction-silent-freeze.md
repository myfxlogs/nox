---
schema_version: 2
id: pagination-direction-silent-freeze
kind: pitfall
risk: high
status: verified
keywords: [增量采集, 分页方向, watermark 冻结, funding_history, okx, 采集器暗死]
scope: [collect/exchange, funding_history, okx, 增量补采]
summary: 增量采集器把 watermark 放进方向相反的分页参数（OKX after=取更早），每轮只重取已知历史——零报错零日志，watermark 冻结数月
first_seen: 2026-09-17
last_verified: 2026-09-17
skills: [事实核验]
evidence_refs:
  - type: decision
    ref: D-321
  - type: doc
    ref: internal/collect/exchange/exchange_history.go:136
  - type: server_fact
    source: cex
    query: GET /api/v5/public/funding-rate-history?instId=CL-USDT-SWAP&after=<ts>&before=<ts>
    observed_at: 2026-09-17T06:40:00Z
---

## 事实/症状

okx `funding_history` 全 symbol 冻结于 2026-08-17（31 天无新行），实时 funding fact 同期正常在采；采集器日志零报错零输出——表面看像「数据不存在」，实为采集结构性空转。

## 根因或策略解释

OKX `funding-rate-history` 分页语义实测：`after=<ts>` 返回**更早**记录、`before=<ts>` 返回**更新**记录（与直觉命名相反）。采集器把增量 watermark（last+1s）塞进 `after` → 每轮请求的都是 watermark 之前的历史区间，dedup 后零新增，watermark 永不前进。被初期手工回填数据掩盖，直到下游消费方（D-318 结算参照）需要新数据才暴露。

同类隐患：分页/增量参数的方向语义不实测就按命名猜（startTime/after/before/since 各家语义不一）；「无错误日志」不等于「采集在工作」——正确的健康信号是 watermark 随时间推进。

## 处置

- 修参数方向：增量首调用 `before=<watermark>`（取更新方向），窗口内向后翻页才用 `after`（D-321）
- 审查同类采集器：凡增量补采，拿 watermark 对照 API 实测方向验证一遍
- 新鲜度监控（D-301a 表级钉）覆盖结算史类表——31 天暗死本应被告警

## 证据

- 实证：`SELECT max(ts) FROM facts WHERE kind='funding_history' AND venue='okx'` → 2026-08-17 08:00
- API 实测：`after=1767225600000`（1-01）→ `[]`（更早方向无记录）；`before=1767225600000` → 返回 7 月记录（更新方向生效）
- 对比：binance 采集器用 `startTime/endTime` 绝对窗口 → 正常推进至当日
