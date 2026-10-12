---
schema_version: 2
id: roundtripper-failover-retry-contract-traps
kind: pitfall
risk: high
status: observed
keywords: [RoundTripper, failoverRT, Clone浅拷, GetBody, 重放, 双提交, bodyless POST, HTTP重试, 幂等]
scope: [internal/httpx, cex-proxy双端点, 任何http.RoundTripper层重试/failover实现]
summary: 在 http.RoundTripper 层做 failover/重试的三个契约坑：Clone 浅拷 URL 指针改写原请求、GetBody 出错后重放已消费 body、bodyless-POST 同签名重放=业务幂等盲区（venue 可能双成交）
first_seen: 2026-09-27
last_verified: 2026-09-27
skills: [KB维护, 事实核验]
evidence_refs:
  - type: decision
    ref: D-519
  - type: commit
    ref: dea18d10
  - type: doc
    ref: internal/httpx/httpx.go:127
  - type: doc
    ref: internal/exec/cex_executor_binance.go:197
---

## 事实/症状

cex-proxy 双端点 REST 回退 failoverRT（dea18d10）复审发现三坑：
① `r2 := r.Clone(ctx)` 后 `r2.URL.Scheme/Host=` 改写——Clone 对 URL 是**指针复制**，改的是原请求；
② `r.GetBody()` 出错被静默吞，r2.Body 仍是已消费的原 body 指针→备端收到空 body；
③ binance 签名请求 body=nil、参数+签名全在 query，`recvWindow=5000` 内同签名重放对 venue 完全有效
——「body 不可重放不触发」防线对 bodyless-POST 完全失效，下单传输错回退=潜在双成交。

## 根因或策略解释

RoundTripper 契约=不得修改传入请求；`Request.Clone` 深拷 Header/Trailer 但 URL 按指针共享。
重试安全判据常被简化成「有 GetBody 就可重放」，但**传输层可重放≠业务可重放**：
幂等性由方法+端点语义决定，bodyless POST（参数在 query）是最易漏的形态。
http.Client 超时类错误伴随 ctx 取消，`r.Context().Err()!=nil` 短路可挡住超时重放，
但连接中途 reset 类传输错不带 ctx 取消，是残余双提交窗口。

## 处置

- ① 改写前 `r2.URL = r.URL.Clone()`
- ② `GetBody()` 出错 → 直接返回原 resp/err，不回退
- ③ 方法门控：POST/PUT/DELETE/PATCH 传输错不回退（风控优先）；读路径 GET/HEAD 全保留
- 落地=D-519 R1 返修（S1/S2），验收锚 A1-A3

## 证据

- review: dea18d10 全量面 race 384 绿（机制本身工作正常，三坑为语义边界非功能性 bug）
- doc: internal/httpx/httpx.go:127-135（①②现场）；cex_executor_binance.go:190,197-201（recvWindow+query 签名形态）
- D-519 复审意见 §三 D1/D2/D3

## 反证条件

- 若 Go 标准库未来把 Request.Clone 改为深拷 URL（查证当期版本源码），①失效
- 若 venue 实证对重复 newClientOrderId 跨终态拒绝（需 venue 文档/实测，不得凭印象），③门控可放宽为显式幂等标注白名单
- GET/HEAD 之外出现幂等 bodyless POST 端点（如 listenKey keepalive）被门控误伤时，按端点显式白名单放行，勿整体放开

## 参考

- D-519 SSOT=docs/D-519-cex-proxy双端点客户端槽-复审意见与R1返修施工提示词-2026-09-27-Claude.md
