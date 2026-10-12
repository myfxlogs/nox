---
schema_version: 2
id: auth-header-curl-verbose-leak
kind: pitfall
risk: high
status: verified
keywords: [curl, -sv, Authorization, Bearer, transcript, token 轮换]
scope: [ops, security]
summary: curl -sv 把 Authorization 头打进工具输出 → token 泄漏；鉴权命令一律 -s + env 引用
first_seen: 2026-08-18
last_verified: 2026-08-21
skills: []
evidence_refs:
  - type: doc
    ref: docs/handoff/LOG.md 2026-08-20 会话纪要
---

## 事实/症状

`curl -sv` 会把 `Authorization: Bearer …` 打进工具输出 → 进入会话转录，违反「token 永不进 transcript」红线（已触发一次 ARB_AUTH_TOKEN 轮换）。

## 根因或策略解释

curl 的 `-v`/`-sv` 标志会将请求头（含 Authorization）输出到 stderr，agent 工具捕获 stderr 后进入会话转录。token 一旦进入转录即视为泄漏。

## 处置

鉴权命令一律 `-s` 静默；token 只经 env 变量引用（`$(command grep -oP '^KEY=\K.*' .env | head -1)`），不显式出现在命令行/参数里。能门禁则门禁，这条至少作为文档纪律。

## 证据

- check: 审查 docs/handoff/LOG.md 2026-08-20 会话纪要
- result: 已触发一次 ARB_AUTH_TOKEN 轮换
- ref: docs/handoff/LOG.md 2026-08-20 会话纪要
- 2026-08-21 重现: @ TRXUSDT | ref: autotrade_logs#1
- 2026-08-21 重现: @ ETHUSDT | ref: autotrade_logs#4
- 2026-08-21 重现: @ AUS200 | ref: autotrade_logs#7
- 2026-08-21 重现: @ TRXUSDT | ref: autotrade_logs#8
- 2026-08-21 重现: @ BRENT | ref: autotrade_logs#9
- 2026-08-21 重现: @ BRENT | ref: autotrade_logs#10
- 2026-08-21 重现: @ AUS200 | ref: autotrade_logs#13
- 2026-08-21 重现: @ BRENT | ref: autotrade_logs#16
- 2026-08-21 重现: @ BRENT | ref: autotrade_logs#17
- 2026-08-21 重现: @ AUS200 | ref: autotrade_logs#20
- 2026-08-21 重现: @ BRENT | ref: autotrade_logs#23
- 2026-08-21 重现: @ BRENT | ref: autotrade_logs#24
- 2026-08-21 重现: @ AUS200 | ref: autotrade_logs#27
- 2026-08-21 重现: @ TRXUSDT | ref: autotrade_logs#28
- 2026-08-21 重现: @ BRENT | ref: autotrade_logs#30
- 2026-08-21 重现: @ BRENT | ref: autotrade_logs#31

## 反证条件

- check: 审查工具链是否仍随 -v 输出请求头
- falsifies_when: 工具链不再随 -v 输出请求头 → 条目 retired

## 适用边界

所有涉及鉴权请求的命令行操作（curl、wget 等）。适用于 agent 会话转录场景。
