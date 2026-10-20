---
schema_version: 2
id: cex-proxy-sni-token-routing
kind: pitfall
risk: low
status: verified
keywords: [cex-proxy, 192.229.85.35, SNI, forbidden, 403, 调试, curl, binance, okx]
scope: [collect, cex-proxy, 调试, venue-adapter]
summary: cex-proxy（192.229.85.35:8443）按 TLS SNI/Host 路由且带鉴权——curl 直连 IP 一律 403 "forbidden"、--resolve 改 SNI 则 SSL_ERROR_SYSCALL；arb-core 走 httpx.Client（CA+token 单源）。调试 binance/okx 形状不要手撸 curl 打代理，改查本机库已采数据或 venue 直连
first_seen: 2026-09-24
last_verified: 2026-09-24
skills: [事实核验]
evidence_refs:
  - type: server_fact
    desc: "curl https://192.229.85.35:8443/api/v5/public/instruments → 403 body=forbidden（10B）；换 Go UA 同拒（2026-09-24T16:0xZ）"
  - type: server_fact
    desc: "curl --resolve fapi.binance.com:8443:192.229.85.35 -k → SSL_ERROR_SYSCALL（2026-09-24）"
  - type: commit
    desc: "internal/dashboard/cex_proxy_health.go:47 「客户端复用 httpx.Client（CA+token 单源）」"
  - type: server_fact
    desc: "对照：okx 主网本机直连 HTTP 200；binance 主网直连 451（地域封锁）——故 binance 必须走代理（2026-09-24）"
related:
  - venue-facts-have-shelf-life
---

## 症状
排查 binance/okx 合约面时手撸 curl 打 cex-proxy：`https://192.229.85.35:8443/...` 返回
403 body="forbidden"；以为 UA 问题换 Go-http-client 仍拒；`--resolve` 指定 SNI 则
SSL_ERROR_SYSCALL 直接断。

## 根因
cex-proxy 是按 TLS SNI/Host 路由的鉴权反向代理，且只认 arb-core httpx.Client 携带的
CA+token 组合。裸 curl 三路（IP 直连/换 UA/--resolve SNI）都不满足，全部拒在门外。

## 教训
调试 venue API 形状的正确路径：
1. 优先查本机库 `fin_cex_universe` 等实采表（venue API 拉取，last_seen 新鲜）；
2. 需直连验证时打 venue 主网域名（okx 直连通；binance 451 地域封锁打不通属预期）；
3. 不要试图用 curl 穿透 cex-proxy——它的准入面不是给你调试用的。

> 2026-10-20 归档（复审席·D-440 顶格退役）：cex-proxy 按 M3 业主令不落生产=死基建，条目随基建退役；触发=backup-r2 条目入册需腾位（索引 98363>98304）。
