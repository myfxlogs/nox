---
schema_version: 2
id: traffic-attribution-per-conn-rate
kind: pitfall
risk: high
status: verified
keywords: [归因, 带宽, 摊算, per-conn, ss -tio, bytes_received, 算术巧合, 连接数非字节账, 字节差分]
scope: [production, network, forensics]
summary: 带宽归因用「总量÷连接数」摊算会产生算术巧合伪证——113 indiv×5.3KB/s≈604KB/s 看似钉死，per-conn 30s 差分实证 indiv 仅 ~90KB/s、大头是聚合/OKX/gate 宇宙面；连接计数≠字节账，定谳必须 per-socket bytes_received 差分
first_seen: 2026-10-22
last_verified: 2026-10-22
skills: []
---

# 带宽/流量归因：禁摊算，必须 per-conn 字节差分

## 病灶（D-663/D-665 实证，2026-10-22）

SG ens17 rx=604KB/s 饱和，票面归因「binance indiv 113 符号×5.3KB/s≈604」
——算术恰好吻合被当成定谳，D-663 按此收窄 indiv 至执行面。
部署后 indiv=1 路，steady 仍 512KB/s：归因错。

`nsenter -t <pid> -n ss -tio` 两次采样 30s 差分 `bytes_received` 才见真账：
OKX Cloudflare 149.6KB/s + fstream !bookTicker 112.4 + stream 75.1 + mt5 28.6
+ churn ≈ 512KB/s。indiv 收窄真实省 ~90KB/s。

## 规则

- **连接计数 ≠ 字节账**。N 条连接存在不代表 N 条均摊流量；
  聚合单连接可多符号（OKX 1 conn 订全宇宙 tickers）。
- **摊算吻合 ≠ 归因成立**。604/113=5.3 的巧合让错误结论过审两轮。
- 定谳法：`ss -tio`（或 conntrack acct）per-socket `bytes_received`
  **双采样差分**÷窗口秒数；单次累计字节会被已关连接遗漏
  （churn 面 bytes 随 socket 死消失）。
- 采样面要盖短寿命连接：ESTAB 快照只数存活者，churn 流量另计
  （用 NIC 总量 − 持久连接差分合计 反推）。
- 部署验证锚（如 ≤200KB/s）必须在**稳态窗**采样——boot 期
  exchangeInfo/补拉 burst 会虚高，≥10min 后再取。

## 姊妹坑

- 同族「连接数好看即健康」：~900 ESTAB 空闲 keepalive 连接
  （逐请求新建 TLS 未复用，~6KB/条），字节小但 conn 泄漏面真。
