---
schema_version: 2
id: mechanical-split-claim-needs-text-diff
kind: pattern
risk: medium
status: verified
keywords: [机械拆分, 重构, 行为中性, 文件搬家, 复审, diff 对比, check-lines, commit message]
scope: [复审流程, 代码拆分, 重构验收]
summary: 施工 commit 声称「纯机械拆分」+机检全绿≠纯搬家——机检测不出重构语义（重构定义为不改行为），唯一能验证纯度的是拆分前后文本对比；复审行为中性类施工必须含文本 diff
first_seen: 2026-09-06
last_verified: 2026-09-06
skills: [验证与交接]
evidence_refs:
  - type: commit
    ref: b51107c
  - type: doc
    ref: docs/7文件超限拆分-复审-2026-09-06-Claude.md
---

## 事实/模式

7 文件超限拆分复审（2026-09-06）：commit 声称「纯机械拆分保行为/API/包边界不变」，机检（build/vet/gofmt/check-lines/test/race）全绿。全文对比核验发现 7 对中 6 对确实是剪切-粘贴（差异仅 import 重组+注释），**1 对（cmd/core/main.go→main_monitors.go）实为参数 struct 提取重构**——18 个局部变量打包成 `monitorsSetupArgs`、函数体加别名行、两处调用表达式加 `a.` 前缀。本例重构经构造点逐字段核对后行为中性成立，但「纯机械」声明与实际不符，且任何机检都不可能发现（重构语义偏差不在测试的检出能力内）。

## 根因或策略解释

「机械」是意图声明不是可验证断言。行为中性改动的定义就是「测试结果不变」，所以机检对它天然盲；施工方在拆分时顺手做等价重构（提取参数对象/改名/表达式重排）是常见且往往无害的，但验收方若把 commit message 当事实，就把「重构审查」漏成了「搬家核对」。能区分搬家和重构的只有文本本身。

## 处置（复审操作法）

1. 快速初筛：符号集对比——`diff <(拆前 | grep -E '^(func |type |var |const )' | sort) <(拆后合并 | 同)`，零差异可初判纯度高。
2. 严格核验：全文对比——`diff <(拆前 | grep -vE '^package |^\s*$' | sort) <(拆后合并 | 同)`；差异应仅为 import 行重组+注释，**任何表达式/声明行变化逐行过目**。
3. 对重构式差异做等价性证明：新引入的 struct/别名，核对构造点字段与原变量一一对应、函数体别名恢复原名、引用替换同值。
4. 拆分类任务的提示词把「机械」写成可验证断言（如「差异白名单=import+注释，出现其他差异行=退回」），而不是意图形容词。

## 证据

- check: `diff <(git show b51107c~1:cmd/core/main.go | grep -vE '^package |^\s*$' | sort) <(git show b51107c:cmd/core/main.go; git show b51107c:cmd/core/main_monitors.go | grep -vE '^package |^\s*$' | sort)`
- result: 75 行差异，其中 18 个 struct 字段声明+别名行+`a.` 前缀替换=重构非搬家；其余 6 对仅 import/注释
- ref: docs/7文件超限拆分-复审-2026-09-06-Claude.md §2

## 反证条件

- check: 拆分类施工提示词含差异白名单断言，或 CI 加文本对比门禁
- falsifies_when: 后续拆分施工在提交前自附文本对比证据，复审不再首抓此类偏差

## 适用边界

一切以「行为不变」为验收目标的施工：文件拆分/搬家/gofmt 类格式化/重命名/提取函数。不适用于声明改行为的任务。
