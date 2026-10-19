---
schema_version: 2
id: form-new-entry-misses-required-field
kind: pitfall
risk: low
status: verified
keywords: [TrxWatchCard, 必填字段, 多提交点]
scope: [frontend, form, validation]
summary: 同 RPC 多提交点时必填清单从后端校验集反推逐核——TrxWatchCard 漏 kind 实证
first_seen: 2026-10-18
last_seen: 2026-10-18
---

## 前端表单新件漏必填字段（同 RPC 多提交点漏抄）

- **实证**：TrxWatchCard save 提交体漏 kind→后端 `kind="" 非法`；watch.tsx 同款表单有默认值故主径不坏，旁路卡片漏抄。
- **教训**：同 RPC 多提交点时，必填字段校验清单应从 proto/后端校验集反推全提交点逐核，非抄一处当全部。
