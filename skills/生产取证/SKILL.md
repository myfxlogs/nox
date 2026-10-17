---
description: 生产取证——验证运行时状态时的强制工作流（SSH→docker logs→psql→venue 实拍三通道交叉），声明运行事实前必走
---

# 生产取证

## 触发条件

需要验证运行时状态时强制触发：

- 部署后验证（代码变更已部署，确认运行时行为符合预期）
- bug 取证（功能异常，需从生产环境取证据定位根因）
- 持仓/swap/报价/风控状态确认（声明当前运行事实）
- 复审运行时验证

关联条目清单：`docs/经验库/技能清单.md` 的「生产取证」节（noxkb gen 生成，指针式，条目为唯一真相源）。

## 前置读取

1. `AGENTS.md §2`（事实优先级：生产实拍 > 代码 > 文档）与 §8（双机拓扑：生产=SG `192.229.85.35`）
2. `docs/技能/事实核验.md`（声明运行时状态的通用规范）
3. 命中的 KB 条目全文（`docs/经验库/技能清单.md` → `docs/经验库/条目/<id>.md`）

## 强制步骤

三通道取证——至少两通道交叉验证，单一通道结论标「待交叉」。生产一律走 SG：

```
SG="ssh -i ~/.ssh/id_ed25519 root@192.229.85.35"
```

**通道① RPC 实测**（SG 上 ConnectRPC，取运行时真实响应）：

钦定径 = `buf curl --protocol connect`（schema 校验径——请求体按 proto 契约校验，错字段在发出前就报；手写裸 JSON POST 已废）。SG 已装 `buf`（v1.68.3，`/usr/local/bin/buf`），`/opt/nox` 下 `--schema .` 直解 buf.yaml 模块：

```
$SG "cd /opt/nox && TOK=\$(grep '^NOX_AUTH_TOKEN=' .env | cut -d= -f2-) \
  && buf curl --protocol connect --schema . -H \"Authorization: Bearer \$TOK\" \
  --data '{\"refresh\":true}' \
  http://localhost:50051/nox.dashboard.v1.DashboardService/ListBrokerPositions"
```

- 服务全名 `nox.dashboard.v1.DashboardService`（proto/dashboardv1/service.proto 为准）
- 认证 `Authorization: Bearer <NOX_AUTH_TOKEN>`——token 从 SG `/opt/nox/.env` 读（app_secrets 库内是密文 value_enc 读不出明文），禁抄入会话
- **grpcurl 已装（v1.9.3 真件）但本服务不可用**：nox-core 是 HTTP/1.1 Connect server 无 h2c，gRPC 协议握手超时——只留作其他 gRPC 服务备用
- `refresh=true` 强制刷新缓存；`fresh=false` → 取证无效，先查采集链

**通道② 数据库查询**（SG pg `localhost:15433`，alphaforge 占 5433——端口勿混）：

```
$SG "docker exec nox-postgres psql -U nox -d nox -c \"
  SELECT kind, venue, symbol, value, ts FROM facts
  WHERE kind IN ('swap_long','swap_short','triple_swap_day')
    AND venue='<venue>' AND symbol='<symbol>' ORDER BY ts DESC LIMIT 10;\""
# 持仓+腿结构 / cashflows 对拍同理（见 docs/技能/生产取证.md 查询样板）
```

**通道③ 日志检查**（SG 上 docker logs）：

```
$SG "docker logs nox-core --since 6h 2>&1 | grep -iE 'panic|error|fatal' | head"
$SG "docker logs nox-core --since 6h 2>&1 | grep -iE 'swap.*cold|reconnect' | tail"
```

- grep 无匹配 = 该类事件未发生（exit 1 = 正常）

**通道④ venue 实拍**（需 venue 权威值时：官方 API/console 直接取证，禁以本系统缓存替代）：

- 腿级数据只认该腿所属 venue 返回值（AGENTS §5）
- venue 凭据从 `app_secrets`/业主域取得，禁在会话/日志/证据中落地明文

## 硬停止条件

- 声明运行时状态但未执行任何通道取证 = 违规
- 单通道结论未标「待交叉」就当作已验证 = 违规
- 取证 `fresh=false` 或查询失败，未先查采集链就下结论 = 违规
- 在会话/条目/日志中写入明文凭据、私钥、完整 DSN = 违规（脱敏后留证）
- 用本机 `localhost:5433`（测试席库）数据充当生产证据 = 违规（生产=SG:15433）

## 验证证据

- 各通道的查询语句和结果摘要（脱敏后写入会话/条目证据节）
- 交叉验证结论（哪两通道吻合，哪通道缺数据）
- 命中条目 `evidence_refs` 含 `server_fact` 类引用

## 产出交接

- 取证结果记入会话上下文与报审/交付文档（STATE/LOG 行或报审文的「生产取证」注）
- 命中 KB 条目的 `last_verified` 更新
- bug 取证结果按 KB 维护规范沉淀为新条目（如有可泛化模式）

## 豁免清单

纯 CSS、纯文档措辞和历史设计解释不强制三通道取证。
