---
name: okx-exec
description: okx 下单径能力账——FreshClient 钱径、clOrdId 定态三分支、歧态势禁补单、h1 钉死传输
---
# okx-exec
## 证据
- 实现件：`internal/trading/exec/cex_executor_okx_order.go`、`cex_executor_status.go`（clOrdId resolve）、`internal/platform/httpx`（FreshClient/assembleRT）
- 测试：exec+httpx 428 绿（含 filled/live-confirm/absent 三分支锚测）
- SG 产验：exec 面 syms=7 订阅活；下单径 FreshClient 在产待命
## 钱径语义
- **FreshClient**：`httpx.FreshClient()`=装配层全继承（CA/tokenRT/failover/banGate）+ `DisableKeepAlives` 每请新 conn——exec/earn rail 的钱径传输正选型
- **下单超时→clOrdId 定态**：传输败 ≠ 业务拒——`httpStatusError` 分流；败后 GET 状态查询按既有 `clOrdId` 定真态
- **三分支**：filled→返成交录 io；live/partially-filled→进确认轨；absent→**同 clOrdId** 可重试（幂等键不新生成）
- **歧态势禁补单**：查询亦败=unknown 态——critical 告警+禁盲重试（补单=双仓险）
## 坑注记
- **clone 携 h2 毒**：见 `okx-rest-boundary` skill——`ForceAttemptHTTP2=false` 不免疫 Clone 浅继的中毒字段；`assembleRT` 已钉 `NextProtos=["http/1.1"]`+`TLSNextProto` 空表
- **裸 transport 禁行**：钱径自造 `http.Transport` 绕过 CA/token/failover/banGate=破口（exec 返修⑤实证）——一律走 assembleRT/FreshClient
- **POST 不回退**：failoverRT 只对 GET/HEAD 重试——非幂等方法主端败即上抛，防备端二次下单
- **WS 下单未迁**：`/ws` trade 域文档有等价面，但缺生产验证——REST 下单为已验证桥，迁移须独立票+协议取证
