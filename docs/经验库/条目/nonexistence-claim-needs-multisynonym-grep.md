---
schema_version: 2
id: nonexistence-claim-needs-multisynonym-grep
kind: pitfall
risk: high
status: verified
keywords: [不存在断言, grep 漏词, 同义词, 中英混检, 命名盲区, 交易日历, cnfi_calendar, 复审误判]
scope: [设计复审, 审计取证, 代码事实回验, 全仓]
summary: 断言「全库不存在某机制」前必须多同义词/多语言 grep（中文+英文+缩写+表名/方法名族），并把结论降级表述为「未找到」而非「不存在」——D-272 复审 C4 单关键词 grep 漏掉 cnfi_calendar/CNFICalendarIsBusinessDay，错误前提写进复审且被设计者沿用，出施工提示词时自审才抓回
first_seen: 2026-09-10
last_verified: 2026-09-10
skills: [KB维护, 事实核验]
evidence_refs:
  - type: review_signature
    ref: docs/D-272施工提示词-2026-09-10-Claude.md §0 A-1
  - type: doc
    ref: internal/store/pgstore/cnfi_calendar_biz.go:12
  - type: server_fact
    source: postgres
    query: SELECT count(*), min(cal_date), max(cal_date) FROM cnfi_calendar
    observed_at: 2026-09-10T16:00:00Z
---

## 事实/症状

复审 D-272 C4 时断言「全库无交易日历」（grep `tradingDay|TradingDay|交易日历|isTradingDay` 零命中），据此接受设计的 trade_date+1 朴素默认公式。出施工提示词前自审再查时发现 `cnfi_calendar` 表 + `CNFICalendarIsBusinessDay` 方法早已存在且 D-270 提醒在用——命名族是 calendar/business_day，与 grep 关键词零交集。错误前提写进独立复审文档，设计 v1.2 沿用「全库无交易日历」理由，两级文档带同一错误事实。

## 根因或策略解释

「不存在」断言的可靠性完全取决于关键词覆盖面，而代码库命名是跨语言跨风格的（中文注释/英文标识符/缩写/表名/方法名族任意组合）。单族关键词命中零 = 只证明「这族词没命中」，不证明「机制不存在」。复审文档的断言有权威光环——下游设计者会直接沿用，错误随文档链放大（设计→复审→提示词三级）。

## 处置

- 断言不存在前，至少跑三族关键词：①中文全称+口语变体（交易日历/日历/交易日）②英文标识符族（tradingDay/businessDay/calendar/holiday）③数据层命名（表名/列名，如表前缀+domain）
- 结论表述降级：「grep X 未命中」≠「不存在」；写文档用「未找到（检索词：…）」并留检索词，让下一级可复核
- 高风险断言（会被设计沿用的）补一条反向验证：`grep -ri "<domain>" --include="*.go" -l` 宽域扫一遍再下结论

## 证据

- check: `grep -rn "tradingDay\|TradingDay\|交易日历\|isTradingDay" internal/`（零命中）vs `grep -rn "BusinessDay" internal/ | head`（cnfi_calendar_biz.go:12 + RepoReminderStore 接口 + D-270 两处调用）
- result: 前者零命中导致误判「无日历」；后者立即证伪
- ref: docs/D-272施工提示词-2026-09-10-Claude.md §0 A-1（勘误责任声明）；生产实测 `SELECT count(*) FROM cnfi_calendar` = 365（2026 全年）

## 反证条件

- check: 对「未找到」类结论补多族关键词 grep
- falsifies_when: 任一族的宽域扫描命中该机制，即证明单族「未命中」结论不可靠

## 适用边界

- 适用于所有「全库没有/不存在/未实现」类断言：设计自审、复审取证、审计报告
- 不适用于：行为性断言（跑测试/查生产即可证伪的），那些本来就以实证为准
- grep 依然是一等工具，本条目只约束「零命中→存在性结论」这一步的推理跃迁
