---
schema_version: 2
id: dedup-baseline-must-be-write-side-not-mutable-state
kind: pitfall
risk: high
status: verified
keywords: [快照去重, dedup 基准, 可变共享态, patch 污染, fin_venue_account_snapshot, rest_poll]
scope: [dashboard/AccountMonitor, 审计留痕, CEX 账户面]
summary: 审计/落库去重的比较基准必须是「上次成功写入的值」（写侧基准），不能用共享可变展示态——后者会被其他 patch 径污染，导致「入库列逐行全同却每轮照写」且判断点永远查不出原因（写入值相同、比较基准已变）。
first_seen: 2026-10-09
last_verified: 2026-10-09
skills: []
evidence_refs:
  - type: decision
    ref: D-136
---

## 事实/症状

D-132 给 REST-only 账户面加 `fin_venue_account_snapshot` 审计留痕，`snapSame` 按入库五列去重后，生产 okx:earn 仍以 ~30s 周期写入**入库列逐行全同**的行（169.39842356 恒定）。

诊断镜像打印 prev/acct 五列对照实锤：`prev.fm=169.39` vs `acct.fm=0`——去重基准 `st.acct`（展示态）被 `OnLedgerAcct` patch 污染：capitalview REST 校准把 `available=equity 回退值` 写进 `balLedger["okx:earn"]`（SrcRest），SetOnPut 回调把 `st.acct.FreeMargin` 从真值 0 改成 169.39；下一轮 REST poll 拿 acct.fm=0 对基准 fm=169.39 → 永远判不等 → 每轮写。同时账户展示面 fm 在 0↔169.39 逐轮震荡。

## 根因或策略解释

- **去重基准选错对象**：`st.acct` 是「当前展示态」，可被多条 patch 径（流帧、账本命中）改写；审计去重的正确基准是「这张表上次写了什么」——写侧自有基准（`snapBase` 字段），与展示态解耦。
- **写侧基准应在写成功后才前进**：写败保旧基准，下一轮同值自动重试，留痕不因瞬时写败永久丢行（悲观基准>乐观基准）。
- **patch 径门禁**：REST-only 键的账本条目本就源自 REST 回写（SrcRest），再回填监控态是循环污染——`OnLedgerAcct` 只对 `venue.HasPrivateStream` 覆盖键 patch。
- 排障要点：库内行值全同 ≠ proto 比较输入相同——比较对象可能是被污染的内存态而非「上次写的值」。临时加一行五列对照日志是最快实证手段。

## 复用面

凡「X 值变才写/Y 变才发」的去重/幂等逻辑：基准 = 上次成功动作的输入副本，禁止用任何会被旁路 patch 的共享活态。
