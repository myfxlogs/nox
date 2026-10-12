---
schema_version: 2
id: quotebus-snapshot-nil-empty
kind: pitfall
risk: medium
status: verified
keywords: [Snapshot, nil, AllLatest, QuoteBus, 列表 RPC]
scope: [collect, rpc]
summary: QuoteBus.Snapshot(nil) 语义 nil=空集非全量；全量用 AllLatest()；列表 RPC 别传 nil 过滤器
first_seen: 2026-08-18
last_verified: 2026-08-21
skills: []
evidence_refs:
  - type: doc
    ref: internal/bus/quote_bus.go:106
---

## 事实/症状

QuoteBus.Snapshot 按 symbols 过滤，nil = 空集；"全量"要用 `AllLatest()`。列表类 RPC 别传 nil 过滤器。

## 根因或策略解释

Snapshot 的 symbols 参数语义是"只返回这些 symbol 的报价"，nil 不代表"无过滤"而是"空集"——返回空切片。这是 Go 中 nil slice 的常见语义陷阱（range nil slice = 零迭代，但按 nil 过滤 = 零匹配）。

## 处置

需要全量报价时使用 `AllLatest()` 而非 `Snapshot(nil)`。列表类 RPC 的过滤器参数为空时，调用方应显式传入完整 symbol 列表或使用 AllLatest 路径。

## 证据

- check: `grep -n "func.*AllLatest" internal/bus/quote_bus.go`
- result: 第 106 行 AllLatest 定义处
- ref: internal/bus/quote_bus.go:106

## 反证条件

- check: 审查 Snapshot 签名/语义是否改为 nil=不过滤
- falsifies_when: Snapshot 签名/语义改为 nil=不过滤

## 适用边界

QuoteBus 内存总线，所有调用 Snapshot 的 RPC 和检测器路径。
