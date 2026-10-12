---
schema_version: 2
id: mtapi-directory-dirty-first-endpoint
kind: pitfall
risk: high
status: verified
keywords: [mtapi, broker, 服务器目录, access, 端点, 内网地址, RFC1918, failover, PingHost, 添加broker]
scope: [dashboard, adapter]
summary: mtapi 服务器目录的 access 端点数组首端点常为内网脏数据（Pepperstone-Demo=10.20.111.43，UK-Demo 二位=192.168.x）——任何「取首端点」逻辑必坑；必须滤 RFC1918+多端点依次试连，PingHost（mt5grpc.Service/PingHost 返回 ms 延迟）可做预排序选最快端点
first_seen: 2026-08-29
last_verified: 2026-08-29
skills: [KB维护, 生产取证]
evidence_refs:
  - type: decision
    ref: decisions.md D-141（根因+修复设计）
  - type: code
    ref: internal/dashboard/broker_rpc_failover.go（parsePublicEndpoints 滤私网+failoverVerify）
  - type: server_fact
    ref: grpcurl mt5grpc3.mtapi.io mt5grpc.Service/PingHost {"host":"192.149.50.250","port":443} → {"result":97}；10.20.111.43 → timeout
repro_or_symptom: 前端向导添加 broker 报「MT5 连接失败 Not connected because of timeout … connecting to server 10.20.111.43:443」——host 来自 SearchBroker 返回 access 的首端点
root_cause: mtapi.io 目录数据含 broker 内网残留地址；broker_search.go firstAccess 仅取首端点传前端，坏首端点后 13 个好端点全被忽略
fix_and_prevention: 端点串全程透传（前端 host 存完整逗号串）→ 后端滤 RFC1918/环回（net.IP.IsPrivate+IsLoopback，域名放行）→ 依次试连首个成功者胜出并回传落库；增强=PingHost 按延迟预排序（D-142 ①）
notes: 连接发起方是 VPS/网关不是用户电脑——用户本地看到的可达 IP 对服务器无参考价值，端点选择必须系统侧自动探测（业主 2026-08-29 订正）
---
