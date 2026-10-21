---
schema_version: 2
id: money-pool-store-no-poschange-kick
kind: pitfall
risk: high
status: verified
keywords: [D-633, stMoney, posChange, OnPositionChange, posreview, position_reviews, pipeline]
scope: [exec-pipeline, event-bus, store]
summary: D-633 钱径池 stMoney 未接 OnPositionChange——pipeline（stMoney）全量 positions 写点不发 posChange kick，posreview/posMon 对钱径写盲，复盘/到期唤醒只认主池写点
first_seen: 2026-10-21
last_seen: 2026-10-21
---

## 钱径池句柄无 posChange 回调：positions 写点双池不对称

- **病灶**：`cmd/core/main.go:240` `st.OnPositionChange = publish(posChange)` 只接主池 `st`；而 `main.go:265` `newExecPipeline(execReg, stMoney, …)` 把 **D-633 钱径保底池** `stMoney` 交给 pipeline——`pgstore.Store.notifyPositionChange()` 回调为 nil。OpenProbe/ApproveProbe 等走 `dashSvc.store`（st）的开仓写点能踢；**pipeline.ClosePosition/ClaimClose/CreatePositionLeg（exec 全径=stMoney）提交后零 kick**。
- **实证（10-21 空仓段预演 pos#1）**：开 PROBE 仓（st 径）→ posMon 卡片即时刷新（kick 通）；09:46 manual_rpc 平仓（pipeline=stMoney 径）→ posreview 停在 posreview.go:61 select 永不醒（pprof 实拍），position_reviews 25min 零行；重启后 boot 首扫一次补齐——**事件径实断、boot 兜底是唯一活径**。
- **波及面**：posreview（CLOSED→review 事件延迟=∞，M4.5 <10min/100% 验收不可达）+ posMon `main:posMonC.posChange`（仓变更不即时刷新）+ recon kick（positions 写点域）。凡 `NewMoneyStore` 句柄承担的 positions/legs 写一律静默。
- **修法方向（决策域）**：stMoney 同接 `OnPositionChange` 到同一 posChange topic（一行装配，与 st 同源），或 `wirePositionKicks` 改双句柄注入；修后须以「开/平各经不同池句柄」对抗锚复验。
- **教训**：拆池句柄（保底池/读面池）必须逐个清点「句柄上挂的进程内回调」——事件回调属装配面非连接面，换句柄即死源。
- **证据**：SG nox-core pprof goroutine dump（posreview parked posreview.go:61）；`docker logs` 09:38 开仓 posMon flush vs 09:46 平仓后 25min 零 posreview 日志；重启后 position_reviews id=1 落行（09:55）。
