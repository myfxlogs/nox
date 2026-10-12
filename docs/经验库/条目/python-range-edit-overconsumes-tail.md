---
schema_version: 2
id: python-range-edit-overconsumes-tail
kind: pitfall
risk: medium
keywords: [python 脚本编辑, find 范围截断, 结构化文档, STATE.md, diff 复核, 行级替换]
related: [structured-doc-edit-swallows-section-header]
status: verified
created: 2026-09-18
updated: 2026-09-18
---

# python find 范围截断跨行吞噬文档尾段

## 症状

用 python `s.find(start)` + `s.find(end_marker, start)` 做区间替换结构化文档（STATE.md 类行导向表格+bullet 混合体）时，若 `end_marker` 选的特征串（如 `'\n|'`）不能匹配紧邻的下一行，find 会**跨行继续向后找**，把中间所有不符合特征的行（bullet 列表、缩进续行、空行）连同目标一起吞掉——表面上替换成功，实际静默删了无关段落。

## 实证

D-327 复审归档时替换 STATE.md 的 D-328 行：`end=s.find('\n|', start+10)` 跳过紧随的 `- 阻塞/待决策:` 等 9 个 bullet/缩进行（均不以 `|` 开头），一路吞到下一个表格行——交接负载尾段（阻塞/下一步/审计结论）被静默删除。git diff 复核（删行数 ≠1）时发现，`git checkout` 还原后改按行索引替换修复。

## 修法/预防

- 行导向文档一律**按行索引**改（`lines[i]=new`），不用字符串区间替换；
- 必须区间替换时，end_marker 用**目标行自身的结尾特征**（如行尾 `|`）或显式 `splitlines` 后按前缀匹配；
- 提交前 `git diff` 核对删行数==预期（本例预期删 1 行，实际删 11 行即暴露）。

## 关联

- 同族不同机制：`structured-doc-edit-swallows-section-header`（锚点行被替换消费致段成孤儿）。
- 同类补救链：pre-commit 门禁不拦结构删除（只管预算/关键词），**diff 通读是最后一道防线**。
