---
schema_version: 2
id: cf-tunnel-outage-diagnosis
kind: pitfall
risk: medium
status: verified
keywords: [cloudflared, Error 1033, tunnel, argotunnel, mihomo, clash, TUN, fake-ip, SNI 阻断, 198.41, 198.18, i/o timeout, TLS handshake EOF]
scope: [部署入口, Cloudflare Tunnel, 代理共存环境, CN 网络]
summary: CF tunnel 1033 排障范式——日志源 IP 辨路径（198.18.x=TUN fake-ip / 真网卡 IP=直连）；域名 DIRECT 规则不覆盖 IP 字面量连接（cloudflared 自建连边缘 IP）需补 IP-CIDR；TCP SYN 通而 TLS 断=SNI 层过滤特征
first_seen: 2026-09-22
last_verified: 2026-09-22
skills: [生产取证, 事实核验]
evidence_refs:
  - type: server_fact
    source: rpc
    query: journalctl -u cloudflared / cloudflared connectivity precheck matrix
    observed_at: 2026-09-22T13:30:00Z
---

## 事实/症状

arb.myfxlogs.org 报 Error 1033（Cloudflare Tunnel error）。cloudflared 日志刷 `TLS handshake with edge error: EOF / i/o timeout`，四路 connIndex 反复重连失败。

## 根因或策略解释

1. **路径辨别先行**：日志 `read tcp <源IP>->198.41.x.x:7844`——源是 `198.18.0.x`（TUN fake-ip 网关，198.18.0.0/15 保留段）说明流量走代理 TUN；源是真网卡 IP 说明裸直连。`198.41.x.x` 是 Cloudflare argotunnel 边缘（198.41.128.0/17），勿与 fake-ip 段混淆。
2. **域名规则的盲区**：clash `DOMAIN-SUFFIX,argotunnel.com,DIRECT` 只匹配带域名上下文的连接；cloudflared 自建连边缘 IP 字面量（自己解析 region*.v2.argotunnel.com），fake-ip-filter 把 argotunnel 排除后连接更是纯 IP——DOMAIN 规则不命中，落进 MATCH 代理组。**要让 IP 字面量也直连须补 `IP-CIDR,198.41.192.0/20,DIRECT,no-resolve`（+162.159.192.0/18 quic 段）**。
3. **SNI 层过滤特征**：TCP SYN 到边缘 :7844 能通（nc 成功）但 TLS ClientHello 一发即断——SNI `*.argotunnel.com` 被过滤，不是端口封死。本次出口侧封锁约 40 分钟后自愈（GFW 波动性）。
4. cloudflared 自带 connectivity precheck 矩阵（DNS/UDP7844/TCP7844/api:443）一次性定界，比自己逐条试快。

## 处置

- 排障序：local core 200 → cloudflared 日志看源 IP 判路径 → `curl :9090/connections` 看 mihomo chains → TCP/TLS 分层探测（nc 验 SYN、curl 验握手）→ 直连全断则等自愈或找能到 CF:7844 的节点。
- 用户级 mihomo 起不来（TUN 需 root）；非 root 实例会占 7890/9090 端口挡 systemd 实例，测完即杀。
- 改代理选择器会把 Devin 自己的外联也挤断——切换前先确认新节点活着。

## 证据

- check: `journalctl -u cloudflared` 源 IP 198.18.0.1（TUN 期）vs 172.168.68.253（mihomo 停后直连期）
- result: 两期同症状→与代理无关；21:33 直连自愈 4 路 lax 注册
- ref: 会话 2026-09-22；config.yaml 已补 IP-CIDR 直连规则

## 反证条件

- check: 直连稳定期加 IP-CIDR 规则后观察重连是否仍 DIRECT
- falsifies_when: 若 ISP 永久性封 argotunnel（多天不自愈），需改 tunnel 固定走可靠出境节点——当前结论仅覆盖波动性封锁场景

## 适用边界

CN 直连环境 + mihomo TUN fake-ip 模式；代理为 redsocks/其他模式时 fake-ip 细节不适用，但「域名规则不覆盖 IP 字面量」通用。
