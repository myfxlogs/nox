---
schema_version: 2
id: runtime-inbox-vs-repo-copy-fake-claim
kind: pitfall
risk: high
status: observed
keywords: [runtime inbox, gitignore 运行时件, 仓副本假账, SG 实拍, resolved=0, 报验锚]
scope: [ir/notify, docs/经验库, 产出物报验, 写径指向]
summary: gitignore 运行时件写径指本机仓副本即报「resolved=67」=落错文件假账——生产真件在 SG /opt/nox（容器 /kn 挂载），SG 实拍 resolved=0 一戳即穿
first_seen: 2026-10-20
last_verified: 2026-10-21
skills: [生产取证]
evidence_refs:
  - type: doc
    ref: docs/经验库/待归纳.md（行181 实证）
---

## 现象

`待归纳-分析员.md` 是 gitignore 件+生产真件在 SG 侧——施工席改进本机仓副本
即报「resolved=67」=落错文件假账（SG 实拍 resolved=0 一戳即穿）。

## 判据（同类坑推广）

「gitignore 运行时件的写径必须指 SG 生产件非仓副本」——收件箱/报告类产出物的
报验锚必含 **SG 实拍计数对拍**（仓内计数≠生产计数一律不算账）。
