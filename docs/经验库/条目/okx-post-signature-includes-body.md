---
schema_version: 2
id: okx-post-signature-includes-body
kind: pitfall
risk: medium
status: verified
keywords: [okx, 签名, Invalid Sign, 50113, POST, body, HMAC, 取证脚本]
scope: [okx REST 私有端点, python 运维取证脚本]
summary: OKX REST 签名串：GET=ts+METHOD+path+?query（无 body），POST=ts+METHOD+path+**raw body**——POST 漏拼 body → HTTP 401 {"code":"50113","msg":"Invalid Sign"}，GET 却正常，易误诊为凭据问题。
first_seen: 2026-08-28
last_verified: 2026-08-28
skills: [生产取证]
evidence_refs:
  - type: server_fact
    source: cex
    query: POST /api/v5/trade/order（签名串漏 body）
    observed_at: 2026-08-28T02:50:00Z
    server_revision: ""
    response_hash: ""
  - type: decision
    ref: D-127
---

## 事实/症状

取证脚本 GET `/api/v5/account/positions` 签名通过；POST `/api/v5/trade/order` 全部 `401 {"code":"50113","msg":"Invalid Sign"}`。同一套 key/secret/passphrase。

## 根因或策略解释

签名 pre-string 拼接规则按 method 分叉：GET 带 query 串不带 body；POST 不带 query、**必须追加 JSON raw body 原文**。脚本统一用 GET 规则拼 POST → 服务端重算不匹配。

## 处置

`pre = ts + method + path + (('?' + q) if q and method=='GET' else '') + (body or '')`——POST 分支显式拼 body。

## 证据

- check: 修正后同凭据 POST 平仓两单成交（ordId 3871580767224320000 / 3871580772257484800）
- ref: 2026-08-28 D-127 #380/#383 孤腿平仓

## 反证条件

- check: OKX API 文档签名章节
- falsifies_when: 官方改为 POST 也带 query/去 body 的不兼容变更

## 适用边界

OKX REST v5；UA 需另过 WAF（见 [[okx-waf-blocks-python-urllib-ua]]）；Go 侧 exec 已有正确实现，本条主用于一次性取证脚本。
