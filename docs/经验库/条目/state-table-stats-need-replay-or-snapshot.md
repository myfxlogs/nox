---
schema_version: 2
id: state-table-stats-need-replay-or-snapshot
kind: pitfall
risk: medium
status: verified
keywords: [末态表, upsert 只存末态, 触发天数, 时序指标, 重放推演, 每日快照, 观察期复盘, 纯函数重算, 历史不可回溯, 指标口径]
scope: [观察期/复盘类指标设计, 任何 ON CONFLICT(key) DO UPDATE 的状态表, 月度/季度回顾取数]
summary: 对 per-key upsert 只存末态的状态表设计"触发天数/逐日分布"类时序指标，在线记录里根本没有历史——row 被每日覆盖，只能查出末次触发日与末值，复盘时数据已丢。两条修路：①信号是底层数据的纯函数时用重放推演（对历史窗口重算，报告必须标注"推演值"）；②约定从当天起每日 SELECT 末态快照存档攒真值。设计观察期监控时先验证存储语义再定指标口径。
first_seen: 2026-09-09
last_verified: 2026-09-09
skills: [KB维护, 市场研究]
evidence_refs:
  - type: doc
    ref: docs/研究/月度复盘预研框架-2026-09-09-ZCode.md（§1.5/§2.1/§6 偏差记录 2）
  - type: code
    ref: internal/collect/cnfi/signals.go（EvaluateSignals/UpsertCNFISignalState 逐日 upsert 同 key）
  - type: server_fact
    source: postgres
    query: "cnfi_signal_state PK=key：9-8 部署后仅存末态行（observed_as_of=末次触发日），无逐日历史可查"
---
