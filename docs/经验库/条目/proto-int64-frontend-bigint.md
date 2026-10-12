---
schema_version: 2
kind: pitfall
risk: medium
status: verified
id: proto-int64-frontend-bigint
keywords: [proto int64, bigint, ConnectRPC, ===, 类型错配, 高亮未命中, JSON 字符串, 恒 false]
scope: [frontend, rpc]
summary: proto int64 到前端（ConnectRPC/TS）是 bigint，JSON 传输为字符串——与 number/string 字面量 `===` 比较恒 false，功能静默失效
first_seen: 2026-08-21
last_verified: 2026-08-21
skills: []
evidence_refs:
  - type: decision
    ref: 修复③ F1
---

## 事实/症状

Broker 页 `#264` 组合徽标跳转持仓页，title 写「跳转持仓 #264」实际停在首行 267——`positions.find(p => p.id === highlight.id)` 中 `p.id` 为 bigint、传入 highlight.id 为 number/string，`===` 恒 false → 匹配未命中 → 无高亮无滚动，页面呈现默认首行。零报错零日志。

## 根因或策略解释

proto int64 无法被 JSON number 精确表达 → ConnectRPC/TS 生态映射为 bigint/字符串；跨边界比较的类型不匹配在 TS 里若一方为 any/未收紧即静默通过。

## 处置

id 比较统一走单一 helper：`sameId(a: bigint|string|number, b)  => a.toString() === b.toString()`（或全程 BigInt 化）；从 URL/路由参数回传的 id 一律字符串化；高亮/锚定类功能必须手测命中路径（静默失效无测试报警）。

## 证据

- check: `web/src/components/PositionsPage.tsx` byId 分支
- result: F1 修复项；根因经类型链推演定位
- ref: 修复③ F1

## 反证条件

- check: 若前端全面启用生成类型的严格比较
- falsifies_when: TS 严格模式 + 生成类型使跨类型 === 编译期报错 → 条目降级为 bigint 常识备注

## 适用边界

所有 proto int64/int64 字段的前端比较/路由参数场景（float 不涉及）。
