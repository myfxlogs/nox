# 能力账 INDEX（全图入口——能力定谳三足规则：官方清单数对账+实测证据+skills 正典）

| 域 | 探测日 | 对象 | 证据件 | 覆盖 | skills |
|---|---|---|---|---|---|
| mtapi MT5 网关 | 2026-10-17 | mt5grpc.mtapi.io:443 × 三真户（15032313 ICR/3001157 EXR/110660304 XMR）+demo 户端点探测 | mt5-rpc-probe-2026-10-17.json（213 行）+ .md（直出表）+ mt5-rpc-raw-2026-10-17.json（60 键原始响应全文） | 74 RPC 清单对账：实测 59 户面+流 12+skipped 7+mutating 未实测 4（真户禁线）+demo 缺端点；Unimplemented 定谳 5 件（PendingOrderHistory/OrderHistoryPagination/MarketWatchMany/Mails/Health） | skills/mt5-account · skills/mt5-quotes-history · skills/mt5-orders-streams · skills/mt5-trading-mutating · skills/mt5-gateway-admin |

**盲区（如实）**：
1. **demo 户 mutating 全谱未测**——Exness-MT5Trial5 端点缺口（Search 解析超时+在册端点 INVALID_ACCOUNT 实拍）；请求箱已挂（fin_condition_gap mtapi-demo-endpoint/credential）；到位后 `tools/mtprobe --demo-env` 补跑。
2. **ChangePassword 自伤型未实测**（改密断访——永久标缺）。
3. **On* 独立流 8 件**为 12-30s 短窗采样（零帧=窗内无事件非故障）；Events 主流 45s 窗。
4. **符号方言**：探针用 XAUUSD 裸名——EXR（3001157）实为 XAUUSDm 型尾缀、XMR 户（110660304）XAUUSD 不存在（证据行在案）；带方言符号的复测=生产 symMap 径。

**关键定谳速览**：token 走**请求体 Id 字段**（非 metadata header——harness 首版 metadata 径全 RPC "Value cannot be null" 实拍）；Connect 需服务器 token 回填后续请求；reflection 全 dump 在证据 JSON `.reflection`。

**复跑**：`go run ./tools/mtprobe --dsn <SG pg DSN> --out docs/能力账 --window 30s [--demo-env /opt/nox-secrets/mt5-demo.env]`（SG 侧执行）。
