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

---

# 能力账索引（D-608——venue 端点全图登记）

| 账 | 件 | 覆盖 |
|---|---|---|
| binance 端点清单 | `binance_endpoints.gen.txt` | 三源并集 773 端点（postman=597/md=341/conn=552/union=773/mutating=205） |
| binance probe 证据 | `bn-api-probe-2026-10-17-o0.pb`/`o150.pb` | 批化 4 批×150+5min 冷却跑毕——`o0`=59 端点（19 ok/23 400/17 404 封禁记忆钉 abort）；`o150`=abort 零行（208B——封禁窗内零覆盖如实钉）；`bn-api-probe-2026-10-17.pb`=批 2/3 同名覆盖伤实证（offset 命名件批 2/3 未带=文件名窗残差在案）；累计覆盖≈137 端点非全谱——禁记忆封禁窗内 repeat 同型钉 |
| binance probe 证据（解禁窗续段重跑） | `bn-api-probe-2026-10-18-o{0,150,300,450}.pb` | 10-18 解禁窗终审开后全量重跑——o0=61 端点（21 ok/17 404/23 400——418 三轮封禁分三次跑成累计，同名覆盖伤在案=pb 仅末轮内容）；o150=150 端点全（76 ok/29 400——三候窗补成）；o300=150 全（55 ok）；o450=118 全（35 ok——清单尾段 568 端点止）；累计≈329 端点本轮覆盖+venue 再裁断 418 abort 记实（生产 IP 限速共享预算=pace 500ms+418 即停纪律守） |
