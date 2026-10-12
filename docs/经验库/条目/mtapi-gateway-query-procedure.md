---
schema_version: 2
id: mtapi-gateway-query-procedure
kind: strategy
risk: medium
status: verified
keywords: [mtapi, mt5grpc, 网关, grpcurl, Connect, id header, OpenedOrders, OrderHistory, 取证, 只读查询]
scope: [adapter, mt5, mt4, cex, ops, probe]
summary: MT 网关（mtapi.io）直连取证——Connect 双 id、token 原样回显、host=MT5 服务器非网关、凭据零回显禁 -insecure
first_seen: 2026-08-24
last_verified: 2026-09-26
skills: [KB维护]
evidence_refs:
  - type: doc
    ref: internal/adapter/mt5.go:81（Connect 实现）
  - type: doc
    ref: internal/adapter/mt4.go:107（id header 路由注释；mt4.go 已于 D-470 退役删除，条目 9-26 复核时该 ref 为历史坐标）
  - type: server_fact
    source: mt5grpc3.mtapi.io
    query: Connection/Connect + MT5/OpenedOrders + MT5/OrderHistory（Exness 277842155 只读）
    observed_at: 2026-08-24
---

## 事实/症状

需要 broker 服务器级只读证据（持仓 swap 字段、历史单字段形状）时，可直连 MT 网关取证，无需写代码：

1. **网关地址不在 broker_accounts**：`broker_accounts.host:port` = **MT5 服务器地址**（Connect 请求参数），网关=第三方 mtapi.io（`mt5grpc3.mtapi.io:443` / `mt4grpc.mtapi.io:443`，core 代码硬编码）。
2. **Connect 双 id**：请求需 `id` header + body 内 `id` 字段（格式 `mdgw-<user>`，网关按 id 路由）；ConnectReply.result = **传入 id 原样回显**（非新 token），后续调用用同一 id 作 header+body。
3. **查询**：`MT5/OpenedOrders`（持仓）、`MT5/OrderHistory`（历史单，`from/to` UTC 格式 `2026-08-20T00:00:00`，`ascending`）——响应是标准字段透传，**缺字段=服务器/网关侧没有该值**（见 [[mt5-spec-vs-settlement-disconnect]]）。
4. grpcurl 直接可用（网关有反射）：`grpcurl -max-time 20 -H "id: mdgw-xxx" -d '{"id":"mdgw-xxx"}' mt5grpc3.mtapi.io:443 mt5grpc.MT5/OpenedOrders`。

## 根因或策略解释

- core 的 adapter（mt5.go:81）每次连接同一网关——这是生产同款路径的只读复现，非新代码。
- 凭据（broker_accounts.password）只在 shell 变量中传递（`PW=$(docker exec ... psql ...)`），输出只留业务数据，零回显。

## 处置

- 查持仓/历史单形状：直连网关（上述命令链），凭据经变量不回显。
- 查系统内 RPC：Connect JSON 端点 `http://127.0.0.1:50051/arb.dashboard.v1.DashboardService/<Method>` + `Authorization: Bearer $ARB_AUTH_TOKEN`（token 变量传递）。
- 查库：`docker exec arb-postgres psql -U arb -d arb -c "..."`（生产只读=复审方职权）。

## 证据

- check: 本会话三连取证（Exness 277842155）：OpenedOrders 无 Swap 键 + OrderHistory 80 行 swap=None + ListBrokerPositions 对照 IC swap=4.99
- result: 网关链路可用，字段透传如实（无 swap=服务器/网关无值）
- ref: 会话 2026-08-24（#264 Exness swap 定案）

## 反证条件

- check: 网关接口变更（id 路由/反射关闭）后命令链失效
- falsifies_when: 网关升级为独立 token 机制（result≠传入 id）→ 本文连接步骤失效，查询端点不变

## 适用边界

- mtapi.io 网关（arb core 硬编码 mt5grpc3/mt4grpc）；其他 MT 网关（自建）需对拍各自协议。
- 仅只读取证；任何写操作（下单/改单）不在本文范围。
- 凭据处理纪律：生产密码只在 shell 变量内流转，禁回显、禁 `-insecure`（TLS 校验必须开启，密码过第三方网关的代价）。
