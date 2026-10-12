---
schema_version: 2
id: error-sentinel-unreachable-past-string-boundary
kind: pitfall
risk: high
status: verified
keywords: [errors.Is, 哨兵错误, err.Error(), msg 字符串, 分类边界, gate, margin insufficient, 告警节流]
scope: [arb-core, exec, autotrade gate, Go error 处理]
summary: 函数返回 (status,msg string) 而非 error 时，err 对象在 err.Error() 处已死——上游设计的 errors.Is 哨兵分类在消费层结构上不可达；分类须降级为 msg 子串约定+告警强制节流
first_seen: 2026-09-25
last_verified: 2026-09-25
skills: [事实核验]
evidence_refs:
  - type: doc
    ref: docs/D-471-D-472设计审计报告-真金层两票-2026-09-25-Claude.md (B-005)
---

## 现象

设计稿在写路径包 `fmt.Errorf("...: %w", ErrMarginInsufficient)` 哨兵，拟在 autotrade gate 层 `errors.Is` 判定后升级告警。审计实证：`intent.Execute(ctx)` 签名返回 `(status, msg string, posID)`——err 对象到 gate 层只剩字符串，哨兵方案在实现点结构性不可达（无 err 可传）。同时该设计漏配节流，同因拒单实测 7 天单类重复数十次。

## 根因或策略解释

`%w` 包裹保的是 error 链；一旦某层签名把 error 拆成裸字符串（常见于「跨执行边界只回传可读原因」的边界函数），下游一切基于类型的分类机制全部失效且**编译不报错**——是静默失效不是编译失败。

## 处置

- 分类点必须与「error 对象存活处」同层：要先在本层判定再字符串化，或上游把分类结果（如 reason-class 枚举）作为返回值一并传出；
- 已只剩字符串的边界：生产端文案约定固定子串（三 executor 统一含 `margin insufficient`），消费端大小写不敏感匹配（覆盖 venue 原文 `Margin is insufficient`）；
- 告警升级必须配节流：per(venue,symbol,reason-class) 窗口 ≥6h 至多一条 warn，重复仅计数落日志表——告警信噪比是「通知真的被看到」的前提；
- 审计同类点：凡「上游包 error、下游判字符串」的链路设计，先查中间层签名是否丢对象。

## 证据

- check: `intent.Execute` 签名 + `gate.go:258` 消费点 + `pipeline_execute.go:94` `%w` 包裹
- result: errors.Is 链可达至 Execute 返回点、其后 err 对象消失；autotrade_logs 30 天 margin 类 FAILED 528 条全为字符串形态
- ref: D-472 SSOT R1 修稿 §S3（@c9627ebe）

## 反证条件

- check: 若 `intent.Execute` 签名改为返回 error 或 (status, reasonClass, ...)，字符串匹配即可退役回类型分类
- falsifies_when: 消费层持有 error 对象——此时应优先 errors.Is 而非子串匹配

## 适用边界

Go 代码库内跨包/跨层的错误分类设计；同样适用于「序列化边界」（RPC/持久化后的错误只剩字符串）场景。
