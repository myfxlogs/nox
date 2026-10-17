# 能力账 INDEX（全图入口——能力定谳三足规则：官方清单数对账+实测证据+skills 正典）

| 域 | 探测日 | 对象 | 证据件 | 覆盖 | skills |
|---|---|---|---|---|---|
| mtapi MT5 网关 | 2026-10-17 | mt5grpc.mtapi.io:443 × 三真户（15032313 ICR/3001157 EXR/110660304 XMR）+demo 户 277842155（mutating 全谱实测） | mt5-rpc-probe-2026-10-17.json（243 行）+ .md（直出表）+ raw 双件=**SG 落点位**（/tmp/mtprobe-out/mt5-rpc-raw-2026-10-17.json——gitignore 防巨件入仓+含未脱敏面；摘要 JSON 已脱敏入仓） | 74 RPC 对账：实测覆盖 59+gap 补 13≈全谱（mutating 3 件 demo 全谱 ok：OrderSend 0.01 BTCUSDm→Modify→Close+pending 挂/改/撤；Unimplemented 定谳 5 件；ChangePassword 自伤型永禁） | skills/mt5-account · skills/mt5-quotes-history · skills/mt5-orders-streams · skills/mt5-trading-mutating · skills/mt5-gateway-admin |

**盲区（如实）**：
1. **ChangePassword 自伤型未实测**（改密断访——永久标缺）。
2. **On* 独立流 8 件**为 12-30s 短窗采样（零帧=窗内无事件非故障）；Events 主流 45s 窗。
3. **符号方言硬坑**：每 broker 后缀不同（EXR/Exness=m 尾实拍、XMR 户 XAUUSD 不存在）——symbol 名一律 venue SymbolList 实读禁猜（skills 三件已锚）。
4. demo 端点已解（18.163.85.196:443 Search(Exness) Access 解析——请求箱 mtapi-demo-endpoint 已 resolved）；raw 全文件 SG 落点（gitignore）。

**PII**：证据 JSON/raw 经 harness sanitizePII 脱敏（UserName/Email/Phone/City/Country/State/ZipCode/UserAddress 值→***）。

**关键定谳速览**：token 走**请求体 Id 字段**（非 metadata header——harness 首版 metadata 径全 RPC "Value cannot be null" 实拍）；Connect 需服务器 token 回填后续请求；reflection 全 dump 在证据 JSON `.reflection`。

**复跑**：`go run ./tools/mtprobe --dsn <SG pg DSN> --out docs/能力账 --window 30s [--demo-env /opt/nox-secrets/mt5-demo.env]`（SG 侧执行）。
