---
schema_version: 2
id: connect-streaming-curl-verify
kind: strategy
risk: low
status: verified
keywords: [connect, server streaming, curl, 信封, envelope, connect+json, oneof, 平铺, 帧解析, 验证, 流式 RPC, grep 污染]
scope: [dashboard, StreamUpdates, e2e, 复审]
summary: connect ServerStreaming 的 curl 端到端验证法——请求体带 5 字节信封（\x00+len+JSON）；响应按信封分帧、oneof 平铺为顶层 key；grep 字段名被其他 case 同名字段污染，统计必须按帧类型隔离
first_seen: 2026-08-23
last_verified: 2026-08-24
skills: [KB维护]
evidence_refs:
  - type: decision
    ref: D-090 复审+部署 / 返修包①复审+部署
  - type: commit
    ref: 01b9912
  - type: commit
    ref: fdf3285
---

## 事实/症状

- 裸 `-d '{}'` 调 connect 流式端点 → 流内返回 `invalid_argument: protocol error: incomplete envelope: unexpected EOF`（HTTP 200，错误在流体内）。
- 对流响应 `strings | grep '"venue"'` 统计报价源分布，得出「过滤泄漏」的错误结论——实际杂音来自 brokerPositions/accounts/personalPositionPrice 等其他 case 的同名字段。

## 根因或策略解释

- connect 流式协议请求与响应都是**信封帧**：1 字节 flags（0x02=end-stream trailer，0x01=压缩）+ 4 字节大端长度 + 载荷；unary 不需要，流式必需。
- protojson 序列化 oneof 时**不包 oneof 字段名**，case 名直接作顶层 key（`{"quote":{...}}` 而非 `{"update":{"quote":...}}`）。
- 流是多类型复用总线（StreamUpdates 10 种 case），字段级 grep 天然跨 case 污染。

## 处置

验证三步法（D-090 双维过滤实证所用的完整流程）：
1. 请求：`printf '\x00\x00\x00\x00\x02{}' > req.bin` + `--data-binary @req.bin`，头加 `content-type: application/connect+json`（`connect-protocol-version: 1` 可选）。**unary RPC 不用信封且 content-type 是 `application/json`**（connect+json 会 415）——信封长度务必程序算（struct.pack），手数字节必错。
2. 分帧：按 1+4 字节信封切响应，逐帧 `json.loads`。
3. 统计：按顶层 key（帧类型）先分桶，再在目标 case 内统计字段——禁跨 case grep。

传输路径（2026-08-24 增补）：**CF 隧道不转发 gRPC**（grpcurl 走 arb.myfxlogs.org:443 得 403 text/html）；本服务 gRPC 直连 50051 也 dial 超时——线上验证一律走 Connect 协议（unary=application/json / 流式=connect+json+信封），与前端同路径。

## 证据

- check: D-090 部署后 curl StreamUpdates 裸体报 incomplete envelope → 补信封后 16KB 帧流；分帧+分桶后 quote 帧恰好仅 venues 所选两源（okx:spot×6+binance:spot×2），逐帧 grep 的「泄漏」结论被推翻
- check: D-091 返修②部署实证——GetAiSettings（unary）用 connect+json 得 415 → 改 application/json 即 200；AskAnalyst（流式）python 组信封（手写长度 0x37≠实际 92 报 proto: unexpected EOF）后 56 delta 帧+citation+done 全解析；grpcurl 公网 403/直连超时
- ref: decisions.md D-090 复审+部署 / 返修包①复审+部署；01b9912 / fdf3285；D-091 返修②复审+部署

## 反证条件

- check: 若 connect-go 未来版本改默认压缩（帧 flags 含 0x01），本法需加解压步骤
- falsifies_when: 裸 JSON 请求体被流式端点接受（协议变更）

## 适用边界

- 适用 connect 协议（connect-go/connect-web）；gRPC 协议（application/grpc*）信封不同不适用。
- 复审/诊断用途；前端行为验证仍以真实 UI 为准。
