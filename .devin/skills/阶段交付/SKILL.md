---
description: 完成 Phase 或关键修复后，按统一格式输出交付总结并写入项目归档
---

# 阶段交付（Phase Handoff）

## 触发条件
- 完成一个设计/施工 Phase（如 Phase G、Phase H）。
- 修复关键 blocker（如 F-1）。
- Claude 指派直施并验收后。

## 禁止
- 不要把交付总结留在私有记忆或聊天上下文。
- 不要只列文件而不写验证结果和下一步。

## 输出位置
1. 主归档：`docs/handoff/LOG.md`（按时间顺序追加；如属补录，标题标注「补录」）。
2. 状态更新：如影响施工表或阻塞项，同步更新 `docs/handoff/STATE.md`。
3. 决策留痕：如涉及方向/架构决策，追加到 `docs/handoff/decisions.md`（D#）。

## 必备章节
1. **背景/范围**：一句话说明本阶段目标。
2. **新增**：新增文件、接口、表、proto。
3. **修改**：关键文件改动点（engine / store / migrations / main.go 等）。
4. **关键修复/决策**：列 D# / 根因 / 证据；如修复了历史 bug，说明「为什么之前会错」。
5. **验证**：go build / go vet / go test -race / e2e / 真连 / 检查项；明确通过或失败原因。
6. **状态/下一步**：✅ 完成 / 🔄 进行中 / ⬜ 待做；下一条可执行动作；阻塞/待决策显式列出。

## 格式约定
- 文件路径与接口名用反引号包裹。
- commit 短码可写，但真相源以 git 为准，不依赖记忆。
- 验证结果用 ✅ / ❌ / 🔄 标注。
- 代码行数/预算提醒：新增文件超过 400 行需自审是否能拆分；超过 800 行必须拆分（AGENTS.md D-023）。

## 示例
参见 `docs/handoff/LOG.md` 中的 **2026-08-08 阶段验收补录**条目（Phase G、Phase H、F-1），它们展示了上述格式在 Audit Attribution、归因闭环、QuoteBus canonical routing 三个场景下的应用。
