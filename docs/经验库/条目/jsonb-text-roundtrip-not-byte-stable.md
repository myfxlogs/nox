---
schema_version: 2
id: jsonb-text-roundtrip-not-byte-stable
kind: pitfall
risk: medium
status: verified
keywords: [jsonb, 规范化, 文本输出, 空格, 逐字比较, 精确断言, JSONB 透传, handler 透传, 语义比较]
scope: [PostgreSQL jsonb, 测试断言, API 透传层]
summary: jsonb 列读回的文本是服务端规范化形态（分隔符后带空格、键序重排），与写入文本逐字不同——透传正确也会被逐字断言判 FAIL；修法=语义比较（两侧 unmarshal 后 DeepEqual），禁在 handler 做 JSON 压缩
first_seen: 2026-09-07
last_verified: 2026-09-07
skills: [验证与交接]
evidence_refs:
  - type: commit
    ref: dd6b320
  - type: doc
    ref: internal/dashboard/service_trx_account_refresh_test.go:83
---

## 事实/症状

TrxAccountRefresh 测试：种子写 `Unfrozen: []byte('[{"amount":1000000,"expires":1700000000}]')`（紧凑），
handler 原样透传 DB 读回值，断言逐字比较 → FAIL，got=`[{"amount": 1000000, "expires": 1700000000}]`
（分隔符后带空格）。handler 行为正确（透传 DB 文本），是断言写法错。

## 根因或策略解释

jsonb 列存储解析后二进制，读回文本=服务端规范化输出：分隔符后带空格、键序按长度+字典序重排、
数字形态归一。写入文本≠读回文本是**常态**（json 列才保留原文）。逐字比较 jsonb 透传=确定性误报。

## 处置

- 断言侧：两侧 `json.Unmarshal` 为 `[]map[string]any` 后 `reflect.DeepEqual`（整数在 float64 范围内精确）
- **禁改 handler** 做 JSON 压缩/重序列化来「过测试」——透传 DB 原文是对的，错在断言
- 需要字节级稳定时用 json 列（非 jsonb），但失去 jsonb 的索引与操作符

## 证据

- check: 逐字断言跑真库
- result: FAIL got 带空格（-count=1 稳定复现，非 flaky）
- check: 语义比较修复后
- result: PASS（R-J2，commit dd6b320）

## 反证条件

- check: 同一种子串在 json（非 jsonb）列上读回
- falsifies_when: 读回与写入逐字相同 → 说明列类型不是 jsonb（另查）

## 适用边界

- 适用于一切「jsonb 读写往返 + 文本级断言」场景（Go/Python/任意客户端）
- 不适用于 json 列（保留原文）；不适用于语义本就该变化的字段（那就不是透传了）
