---
schema_version: 2
id: incident-fingerprint-triage-runbook
kind: ops
risk: high
status: verified
keywords: [单腿裸露, LEGGED, legged_unwind, 孤儿腿, 历史查证无命中, starvation, 饥荒看门狗, incident unacked, STRATEGY_EXHAUSTED, 处置手册, D-174]
scope: [internal/exec, internal/autotrade, 告警分诊, 人工处置]
summary: 四族事故指纹分诊手册——单腿裸露告警先于自愈（混合腿走独立平仓链非 autoFlatten）、孤儿腿多为查证时窗竞态非真分叉、starvation 先看 facts/quote 上游链断再判调度挂、incident unacked 与 STRATEGY_EXHAUSTED 均人工恢复域
first_seen: 2026-10-10
last_verified: 2026-10-10
skills: [生产取证, 事实核验]
evidence_refs:
  - type: decision
    ref: D-174
  - type: doc
    ref: internal/exec/pipeline_execute_finalize.go:42
  - type: doc
    ref: internal/exec/pipeline_resume_helpers.go:135
  - type: server_fact
    source: postgres
    query: SELECT id,status,exit_kind FROM positions WHERE id IN (505,507,508,509); position_legs/autotrade_logs/app_config 对拍
    observed_at: 2026-10-10T11:00:00Z
    server_revision: SG pg:15433
    response_hash: pos507/508=legged_unwind 11-31s 自愈; ack_id 507→508 后 #509 OPEN; autotrade_state=RUNNING
---

## 事实/症状

D-172 分诊真信号转票候补 10 行聚为四族（D-174 核查定性）：

1. **单腿裸露**：pos507（9-30）/pos508（10-06）「单腿成交裸露：1/2 腿已成交/单边暴露需手动处置」critical。
2. **孤儿腿**：pos505 第 1 腿 gate:perp「券商侧已无此腿且历史查证无命中（票号…）」critical。
3. **starvation 三连**：swapArb factsKick×11 / meanRevert mrKick / exit-eval positionLoop 各一。
4. **incident unacked pos508 ×3 + STRATEGY_EXHAUSTED**：事故仓未确认拒开 + 全策略禁用降级。

## 根因或策略解释

**①裸露=告警先于自愈，非执行 bug**。507/508 均 MT 腿 FAILED+CEX 腿 FILLED→LEGGED；`finalizeOpenPostLegs` 对**含 MT 腿组合不走 autoFlatten**（注释钦定「含 MT 腿的组合不动，SWAPARB 有独立平仓链路」），只发 critical。独立平仓链（reconcile autoClose/ResumeClose）11s/31s 自动平掉成腿（exit_kind=legged_unwind，pnl −4.25）——critical「需手动处置」与秒级自愈并存=文案误述。**处置手册缺位**：告警未告知「先等 30s~5min 看自愈」。

**②孤儿腿=查证时窗竞态非真分叉**。pos505 gate 腿 venue 侧仓位帧（futures.positions size -10000→-8390→0）与平仓帧持续在流，我方 CLOSE FILLED 延迟至 16:35 落账——「券商侧无此腿」是 backfill 双源（cexCloseFill/pnlCalibrator）在该时点查不到平仓证据的瞬时真值，**非账本/venue 真相分叉**。gate 走 contract+ClosingStartedAt 窗回填（D-577），窗口起算/帧序竞态可致双源皆空→critical 先于终态落地。

**③starvation=上游链断非调度挂（主证），偶发环堵（次证）**。facts 表 swap_long/swap_short/funding 在 10-01 03:00→10-02 01:00 **连续 22h 零写入**（前段 2894/1133 衰减），与「swap 采集整轮零产出 err=89/90（venue RPC 错）」同日——factsKick 超时未到=kick 生产侧死，看门狗**正确报断**。mrKick/exit-eval 各一发=扫描/评估环被慢调用（store flush/DB）堵住的瞬时实证，未见复发序列。

**④unacked+exhausted=人工恢复域，机制正常**。508 LEGGED→legged_unwind 后 incidentGate 拒开×3（ack_id=507）；业主人工 ack 至 508 → #509 于 00:56 OPEN。STRATEGY_EXHAUSTED（10-03）后 D-082 人工启用 XASSET_CARRY 并复位 RUNNING（autotrade_state.reason 实证，仅人工恢复=钦定语义）。「#509=疑似人工恢复」**已证**。

## 处置（人工处置手册）

| 告警 | 第一步 | 判定 | 行动 |
|------|--------|------|------|
| 单腿成交裸露（critical） | `SELECT status,exit_kind FROM positions WHERE id=#N` + legs CLOSE 行 | 已 CLOSED+legged_unwind → 自愈完成 | 只 ack；30s~5min 未 CLOSED 才人工查 venue 侧残腿（生产取证三通道） |
| 券商侧已无此腿 | legs 该腿有无 CLOSE FILLED + cex_private_events 平仓帧 | 有 CLOSE→延迟落账非孤儿 | 查 venue 仓位帧（gate futures.positions）证真孤儿才按账本分叉立票 |
| starvation（factsKick） | facts 表按小时 count 断点 + swap 采集告警同日对时 | 上游零写入→采集链死 | 查 venue RPC/采集器，非调度器 |
| starvation（mrKick/exit-eval） | 是否复发序列 | 单发→瞬时环堵观察；连发→查 positionLoop 阻塞调用 | 连发立修复票 |
| incident unacked | incidentGate 拒开原因行 | 事故仓未 ack=风控正确拦截 | 核实事故仓处置完 → app_config ARB_RISK_INCIDENT_ACK_ID 推进 |
| STRATEGY_EXHAUSTED | strategy_stats.disabled 清单 | 全禁=仅人工恢复钦定 | 业主裁定启用某策略+SetAutotradeState RUNNING |

## 证据

- check: SG pg `SELECT id,status,exit_kind,opened_at,closed_at FROM positions WHERE id IN (502,505,506,507,508,509)` + position_legs/autotrade_logs
- result: 507/508=MT FAILED+okx FILLED→legged_unwind 11-31s CLOSED；508 后 3×REJECTED unacked→ack 508→#509 OPEN 00:56；505 legs OPEN+CLOSE 双 FILLED
- check: facts 小时聚合 10-01 03:00→10-02 01:00 零行
- result: 22h 生产链断，与 venue RPC err=89/90 同日
- ref: D-174（LOG 四族定性节）

## 反证条件

- check: 再现「裸露告警后 >5min 无 CLOSED」或「venue 帧证仓位存续而我方 CLOSE 永不落账」
- falsifies_when: 出现上述任一行 → 自愈链/对账径真 bug，立修复票

## 适用边界

适用于 XASSET_CARRY 混合腿组合（MT+CEX）。纯 CEX 组合走 autoFlatten 另径；MT venue 无 cex_private_events 兜底，孤儿判定只能用 venue REST 复查。
