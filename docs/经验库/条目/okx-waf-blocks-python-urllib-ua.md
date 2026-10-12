---
schema_version: 2
id: okx-waf-blocks-python-urllib-ua
kind: pitfall
risk: low
status: verified
keywords: [okx, 403, 1010, WAF, User-Agent, python, urllib, 脚本取证]
scope: [okx REST API, 运维取证脚本, python urllib]
summary: OKX 公网 API（www.okx.com）WAF 拦截 python urllib 默认 UA「Python-urllib/3.x」→ HTTP 403 body "error code 1010"；Go 客户端正常。取证脚本必须自定义 User-Agent。
first_seen: 2026-08-28
last_verified: 2026-08-28
skills: [生产取证]
evidence_refs:
  - type: server_fact
    source: cex
    query: GET https://www.okx.com/api/v5/account/positions (python urllib 默认 UA)
    observed_at: 2026-08-28T02:55:00Z
    server_revision: ""
    response_hash: ""
  - type: decision
    ref: D-127
---

## 事实/症状

生产取证脚本（python urllib，正确签名）调 `/api/v5/account/positions` → `HTTP 403 error code: 1010`；同宿主 core 的 Go 客户端同一端点正常。加 `User-Agent: Mozilla/5.0 ... arb-ops/1.0` 后立即 200。

## 根因或策略解释

OKX 前置 WAF（Cloudflare 系）基于 UA 浏览器签名黑名单拦 `Python-urllib/*`；与签名/凭据无关（签名错是 401 50113）。

## 处置

python 脚本一律带自定义 UA 头。curl 默认 UA 可过。

## 证据

- check: 同一脚本去掉 UA 头复现 403/1010，加回头 200
- ref: 2026-08-28 D-127 止血 okx 孤腿平仓会话

## 反证条件

- check: OKX 文档变更或 WAF 策略放宽
- falsifies_when: 默认 UA 也能 200

## 适用边界

OKX 公网 API（含 x-simulated-trading:1 demo）；不影响 Go http 客户端（core 无此问题）。
