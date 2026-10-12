---
schema_version: 2
id: prompt-string-match-needs-persisted-sample
kind: pitfall
risk: high
status: verified
keywords: [提示词, 施工规格, 字符串匹配, LIKE, 落盘格式, reason 前缀, 真实样本, server-facts]
scope: [autotrade, 设计提示词, 复审, SQL]
summary: 提示词含字符串匹配/格式断言时必须贴生产落盘真实样本——按代码路径推导的格式≠实际落盘格式（调用侧前缀等转换层会让 spec 全部失配）
first_seen: 2026-09-08
evidence:
  - type: commit
    ref: "8c10ff2（S4 热修）——LIKE basis loss% / net swap% 对落盘 exit-basis-loss 前缀 0 命中"
  - type: db
    query: "SELECT reason FROM autotrade_logs WHERE status = CLOSE_TRIGGER —— 482 行 9-7 起即带 exit 前缀"
    observed_at: 2026-09-08
---

## 事实/症状

D-256-A S4（组合止损计数）设计时从 `engine_position.go` 的 shouldClose reason 字符串推导出 LIKE 匹配模式（`basis loss%`/`net swap%`），施工照图实现+模拟 map 测试全绿；上线后计数器对生产数据 **0 命中**——实际落盘 reason 带 `exit: ` 前缀（logCloseTrigger 调用侧拼接），一条 SELECT 就能看到。

## 根因或策略解释

代码里函数返回的字符串 ≠ 最终落盘字符串——中间有调用侧前缀/包装/截断等转换层。设计时只读了构造点没读写入点，且测试用 mock 夹具（自造格式）而非真实格式，双层失守。与 [[server-facts-over-docs]] 同源：**规格断言属于事实断言，必须以落盘事实为准**。

## 处置

- 硬规矩：凡提示词/设计含字符串匹配、格式断言、字段语义 → **先拉生产真实样本贴进设计文档**（D-211「附录样本必须 curl 原文粘贴」条款的泛化）。
- 测试规格强制：此类逻辑必须「DSN 门控+真实格式夹具」，mock-only 测试不得复审通过。
- 已入 Claude 私有记忆 self-audit-before-prompt（F6 规则①②）。

## 证据

- check: 热修后生产验证 SELECT 返回 IC×Exness 配对计数=2/3（482+483 两笔止损入账）
- ref: 会话 2026-09-08（D-256-A 复审+483 锚验收顺带热修）
