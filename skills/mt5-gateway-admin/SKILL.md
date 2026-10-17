---
name: mt5-gateway-admin
description: mtapi 网关连接/服务面——Connect token 语义/Service 族/reflection dump/权威源对拍纪律
---

# mt5 网关服务面（实测 2026-10-17，证据=docs/能力账/mt5-rpc-probe-2026-10-17.json）

| RPC/面 | 功能 | 实测 | 关键坑 |
|---|---|---|---|
| Connection.Connect | 连接+登录（返回 token） | 三户 ok（256-500ms） | **token 语义**：返回值回填后续每 Request 的 `Id` 字段（**非 metadata header**——header 径全 RPC "Value cannot be null" 实拍，首版 harness 教训）；多 fallback 端点 mt5grpc1/4/2.mtapi.io |
| CheckConnect | 连接活性 | 独立连接 id 径（probe skipped 如实） | 断线自愈=adapter sessionClient 重连包装 |
| ConnectEx/ConnectProxy | 连接变体（扩展参数/代理） | skipped 如实缺 | probe 走 Connect 主径已证通；变体未单独实测 |
| PingHost(Many) | 服务器端点探活 | PingHost ok；Many 版 err「host.Length != port.Length」 | Many 版入参=host/port 配对数组，参数形态坑在案 |
| GetClients/MemorySnapshot | 网关管理面 | err「Unable to cast…」/「Unknown result type」 | 网关切型/序列化缺陷实拍——管理面不可用如实 |
| Disconnect | 断会话 | probe 收尾在用 | — |
| Service.Ping/Version | 网关活性/版本 | ok | 无 token 面 |
| Service.Health/GetDemo | — | Health=Unimplemented；GetDemo=side-effect（开 demo 户）标缺 | GetDemo 会真开 demo 账户——业主令禁自开户 |
| Service.Search | 公司→服务器/地址解析 | ok | demo 端点正径：`Search(company="Exness")`→Results[].Access 列表（**host:port 须拆分**——整串当 host=Connect disposed/timeout 实拍）→Connect 户主凭据；实拍 MT5Trial5=18.163.85.196:443 |
| reflection | 服务/方法/消息/枚举全 dump | 证据 JSON `.reflection` 节 | **补「名单 grep」缺口**的正道——新增网关版本能力 diff 用它 |
| 权威源 | /opt/grpc-proto/mt5/protos/mt5.proto | 2026-10-17 对拍：仓内副本 30 行 diff 全注释级（go_package 定制+ClusterDetails 注释块）——枚举/语义零漂移 | 仓内副本随权威源修（D-604 对拍纪律） |

**能力定谳三足**：官方清单数（reflection+权威源 proto）×实测证据（能力账 JSON）×skills 正典（本目录）——三者同源对拍。
