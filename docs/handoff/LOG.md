# LOG.md — 阶段交付归档（NOX 仓首条）

## 2026-10-12 M3 切流交付（业主令窗口）

### 背景/范围
D-212 重建系收官段：SG 生产机停旧 arb 全套、以全新 nox 名与目录上线（业主令「换，全换为新的名称」）。实施=本席（决策席）按 `docs/搬迁清单.md` §8 顺序执行。

### 切流实序（SG 生产实拍）
1. `docker stop arb-core`（保 Exited 回退备件）→ `pg_dump arb | gzip` → `/opt/nox-backups/d212-archive/arb_pre_m3_20261012_1407.sql.gz`（245MB，兼 DROP 前置归档）
2. 停 `arb-postgres` → 卷克隆 `docker volume create nox_nox-pgdata` + alpine `cp -a`（D-241⑦ 防空卷灾径）
3. `docker compose -f docker-compose.yml -f docker-compose.sg.yml up -d nox-postgres`（`127.0.0.1:15433`）
4. `ALTER DATABASE arb RENAME TO nox` + `ALTER ROLE arb/arb_ro RENAME TO nox/nox_ro`（临时超管径——session 用户不可自改名；`nox_ro` 已被影子迁移 1004 集群级预造，arb_ro 走 DROP OWNED+DROP 清残）
5. `TRUNCATE schema_migrations` + 写 1000 marker → nox-core 启动自动跑 1001–1004（51 个 NOX_ 键落、7 张不搬表 DROP）
6. `/opt/nox/.env`：`ARB_*`→`NOX_*` 全量改名留值，`MASTER_KEY` 原值搬，proxy 键不落（生产直连）
7. `nox-core` 起 `env=live` healthy（`127.0.0.1:50051`），123 员工心跳/3min、740 ticks/5min
8. cron 六行全换（/opt/nox 路径、NOX_PG_DSN、nox 库、/opt/nox-backups）
9. `cloudflared-arb.service`→`cloudflared-nox.service`（同 token；`arb.myfxlogs.org` 实拍 200 通——`nox.myfxlogs.org` 路由在 Cloudflare dashboard=业主域）

### 对等闸（§7）
- ② 测试函数级对拍：归档 3376 vs 新 3233，150 缺件全归钦定族（cmc50/dashboard63/scan-intra15/pgstore-hedge-intra-pricealert11/notify8/core-intra1/digest1/testutil 改名1）✅
- ③ 名册：归档 47−4 裁汰=43，diff 精确 ✅（影子 122+ beat 实证）
- ④ RPC：归档 115−8=新仓 107，零新增，前端零死引用（`is_arb` 钦定域值）✅
- ⑤ SG 表：95 行=81 业务表全有属+7 DROP 覆盖+6 基建（pg_stat×2/schema_migrations/视图/分区×2）✅
- ⑥ 影子容器：44min 只读探活零 critical（克隆库+demo 域+飞书键删=零外侧面）；迁移 1000–1004 实战彩排全过 ✅
- ⑦ import 210/210 ✅；① build/vet/gofmt 绿，全量套判=dashboard 负载敏感超时 flake（D-234 同型；独立 21.8s/774 测全过）

### 关键修复（切流热修，D-243）
`dciSubscriptions` 端点 `/v1/dci/product/subscriptions` **生产实拍 404 不存在**（Binance DCI 无此端点）——改证 `positions?status=SETTLED`（credread 签名实测返 #7 行 `id=18537557`）；匹配键真值=订单 `response.positionId`（`product_ref` 是产品号非仓位号，此前 HOLDING 段即从未真匹配）；`settleAsset` 优先于 `exercisedCoin`（#7 未行权 PUT 到账=USDT 非 BTC）；利息=`settleAmount−subscriptionAmount`。改前 #7 曾以错值（ccy=BTC/0/0）翻态——**账面已 SQL 补正**：`settle_ccy=USDT`、`settle_income=1.4066`、`income_events dci_interest tran_id=71`。`SubscriptionsFn`→`SettledPositionsFn`，33 测全绿。教训：venue 端点名禁凭印象写——以官方文档+签名实拍为准。

### 验证
- `go test ./internal/trading/dciexec/` 33/33 ✅（含真实 Binance 形 positionId+settleAsset 锚例）
- 生产：#7 `filled→settled` 实翻（14:20 UTC）；#8 settleDate 2026-10-13 未到期留待自动
- `https://arb.myfxlogs.org/` 200 通（打 nox-core）
- 9 critical 告警=周日冻结腿+xasset ticks 切流空窗（环境性非缺陷）

### 状态/下一步
- ✅ SG 生产=NOX 全量运行；arb_* 容器 Exited+旧卷+归档 dump=回退备件（保留期按票）
- ⬜ `nox.myfxlogs.org` 域名路由——业主 CF dashboard 件
- ⬜ #8 明日自动翻 settled（同径已实证）
- ⬜ 下票=MT4 平台概念退役（D-240；前置已满足）
