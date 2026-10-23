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
## 鉴权与方言（重建第一日件）
- **REST 签名**：`signutil.OkxSign(secret,ts,method,pathWithQuery,body)`=`base64(HMAC-SHA256(secret, ts+method+path含query+body))`；四头 `OK-ACCESS-KEY/SIGN/TIMESTAMP/PASSPHRASE`；ts=ISO8601 毫秒串。demo 户加头 `x-simulated-trading:1`。
- **下单方言**：`tdMode`=cash〔spot〕/cross〔perp 全仓〕〔isolated 码内有备未产验〕；`posSide`=net〔单向持仓户〕vs long/short〔双向户——arms 簿须与账户持仓模式一致〕；`instId`=`BTC-USDT-SWAP`〔perp〕/`BTC-USDT`〔spot〕原生键。
- **WS 下单未迁**：见钱径节——文档有等价面但产验缺，REST 下单是已验证桥。
## 错误码族谱（docs-v5#error-code 取证 2026-10-23——总表 50000-53999，子码形 `51008_1000`）
- **50004 超时歧义**：「接口请求超时（不代表请求成功或失败，请检查请求结果）」——与 clOrdId 定态轨同语义：传输/超时败≠业务败，**必查单定真态**，禁直接重试。
- **鉴权族 501xx**：50101 key/环境不匹配、50102 时间戳过期〔**UTC 偏差=最常见因**，装表须 NTP〕、50110/50121 IP 白名单〔业主配合域〕、50113 签名无效〔sign_string 序错——ts+method+path+body〕、50120 权限不足〔提币/划转域权限缺——gate 同族病灶先例〕。
- **交易类 51xxx**：51000 参数错、51001 instId 不存在〔目录脏符触发——SILVER-USDT-SWAP 实证〕、51003 ordId/clOrdId 至少填一、**51008 可用余额不足**〔子码 _1101/_1103=杠杆档位持仓上限——调杠杆或减张〕、51131 仓位可用余额不足。
