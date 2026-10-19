---
schema_version: 2
id: facts-key-shape-dual-standard
kind: pitfall
risk: medium
status: verified
keywords: [facts, 键形, symbol#N, 写读口径]
scope: [pgstore, facts, key-domain]
summary: facts 写键形↔读谓词须同账——sessions 写 symbol#N 段键/读裸名=有行必查空；新 kind 落地必对拍
first_seen: 2026-10-18
last_seen: 2026-10-18
---

## facts 键形双标准：写 `symbol#N` 段键、读裸名精确匹配=有行必查空

- **实证**：sessions 写 `symbol#N` 后缀段键，FactsSince 消费方用裸名精确匹配→「有行必查空」。
- **教训**：facts 写方键形与读方谓词须同账约定（键带段缀时读方须 LIKE 前缀或写方补裸键汇总行）——新增 kind 落地时「写键形↔读谓词」对拍是必检项。同族=键域错位族（第 N 例）。
