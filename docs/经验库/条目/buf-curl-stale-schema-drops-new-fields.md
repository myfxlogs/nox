---
schema_version: 2
id: buf-curl-stale-schema-drops-new-fields
kind: pitfall
risk: medium
status: verified
keywords: [buf curl, proto 滞版, 假缺实真]
scope: [rpc, forensics, proto]
summary: buf curl --schema 按本地 proto 解码——SG proto 滞版静默弃显 wire 新字段；断言前先核同步态
first_seen: 2026-10-19
last_seen: 2026-10-19
---

## 生产取证坑：buf curl schema 滞版静默弃显 wire 新字段

- **实证**：GetOverviewBundle 加 principalAvailable/idleStreakAvailable 部署后，SG 上 `buf curl --schema .` 按滞版本地 proto 解码——wire 上的新字段被静默丢弃，呈现「假缺实真」，两轮排查才定位 proto 同步缺口。
- **规则**：生产取证 RPC 断言新字段存在与否前，先核 SG proto 文件同步态（deploy_sync 推码后再 curl）；或直接用已部署二进制侧证据（strings/行为锚）交叉。
