---
schema_version: 2
id: probe-ua-cf1010-misread-as-no-route
kind: pitfall
risk: high
status: verified
keywords: [Cloudflare 1010, error code 1010, 403 实探, User-Agent, 探针误诊, cex-proxy, OKX, IP 白名单, mihomo, 出口轮换, 50110]
scope: [reconcile, collect, CEX, cex-proxy, 网络诊断, mihomo]
summary: 无 UA/工具特征探针触发 Cloudflare 1010 被误诊为「代理不路由」→ 代码绕行直连后流量被宿主代理分流到轮换节点 IP，击穿 CEX key IP 白名单（OKX 50110）；诊断反代路由必须用带真实凭据+常规 UA 的请求
first_seen: 2026-09-22
last_verified: 2026-09-22
skills: [事实核验]
evidence_refs:
  - type: decision
    ref: D-395
  - type: commit
    ref: 32d9f513
  - type: doc
    ref: internal/reconcile/cex_account_subaccounts.go:112
  - type: server_fact
    source: http_probe
    query: 真签名 GET /api/v5/asset/balances 经 https://192.229.85.35:8443 → 200；无 UA → CF 403 error code 1010
    observed_at: 2026-09-22T14:20:00Z
---

## 事实/症状

- 9-20 实探结论「okx 资金/理财端点经 cex-proxy 403 不路由」→ 代码把 `okxSubaccountBase` 写死直连 `www.okx.com`（D-363 核证点④）。
- 9-22 生产 account-monitor 对 okx:funding/earn 每 ~10s 报 401 `50110 IP not in whitelist`，报错中的源 IP 在两个陌生地址间轮换（189.24.x/43.249.x），既非本机 IP 也非代理 IP。

## 根因或策略解释

双层叠加：

1. **误诊层**：当时的 403 实为 Cloudflare `error code: 1010`（ASN/UA 级 bot 拦截，无 UA 或工具特征探针触发），不是「路由不存在」。9-22 真签名复测：同端点经代理带 `Go-http-client` UA 返回 200 真数据——代理路由与 IP 白名单一直正常。
2. **诱发层**：直连 `www.okx.com` 的流量被宿主机 mihomo 按域名分流进轮换节点（mihomo 重启后生效），OKX 看到的源 IP 逐请求漂移 → 白名单 50110。**凡依赖「固定出口 IP + 对端白名单」的链路，宿主机透明代理/分流规则是隐形破坏源**——出口不可枚举时白名单必失效。

## 处置

- 修复：子账户签名请求改 `config.base_url`（cex-proxy 固定白名单 IP）优先、直连兜底（commit `32d9f513`）；部署后 10min 零 50110。
- 诊断规范：判「反代是否路由」必须用**带真实签名+常规 UA** 的请求复测；`error code: 1010`/`1020` 是 CF 边缘拦截页（响应体即纯文本错误码），不是上游应用响应。
- 出口审计：主机上有透明代理（mihomo/clash 类）时，凡 CEX IP 白名单链路必须确认目标域名的分流出口固定。

## 证据

- check: `curl --cacert proxy-ca -H X-Proxy-Token …/api/v5/asset/balances`（无签名）→ OKX 50103（到达应用层）；带真签名+Go UA → 200；Python-urllib 默认 UA → CF 1010
- result: 部署后 account-mon okx:funding/earn 轮询静默通过，50110 归零
- ref: D-395 / commit 32d9f513

## 反证条件

- check: 若再次出现经代理的 OKX 私有端点 CF 1010（带常规 UA）→ 代理 ASN 被段封，「走代理」结论需重审
- falsifies_when: 带真实凭据和浏览器/Go UA 经代理仍持续 403 1010

## 适用边界

一切经 CDN/WAF（Cloudflare 等）前置的 API 探测诊断；本机或链路中存在透明代理分流的环境。
