# LOG.md — 阶段交付归档（NOX 仓首条）

## 2026-10-13 M4.2 终审交付：策略生命周期状态机+机读判据表+提案 SLA+晋级自动执行（c6de485e）

### 背景/范围
v3 §7 第二阶段：布尔 disabled 升格六态状态机（candidate→paper→graduated→live | demoted→retired）；淘汰判据迁表零字面量；proposed 提案 SLA 限期翻态；live 策略提案自动执行——`go N` 退为投委会晋级批准面（批策略一次非每单）。

### 新增
- 迁移 `1009_strategy_lifecycle.sql`：`fin_strategy`（六态 CHECK+hit_rules+decided_at/by）+`fin_strategy_criteria`（strategy/kind/pred/params jsonb，UNIQUE 键）+`app_config.proposal_sla_hours`=24。
- `internal/store/pgstore/strategy_lifecycle.go`（261 行）：SetStrategyState/GetStrategyState/EnsureStrategyRows/ListStrategiesByState/ListStrategyCriteria。
- `internal/portfolio/lifecycle/criteria.go`（111 行）：FireCriteria 判据求值器（区间门+conds 谓词+mode all/any，metric 无证恒 false fail-closed）。
- `internal/trading/dciexec/autoexec.go`：AutoExecLive（ExecTemplates 白名单→GetStrategyState=='live'→GoProposal，行缺/读败/非 live 全 fail-closed）。
- `internal/research/oppmatch/lifecycle.go`：runLifecycle 三件并入扫描拍（覆盖确保/SLA/晋级评估），零新定时器。

### 修改
- `elimination.go`：三段字面量判据→`NewEliminator` 每判实时读表（读败/空表→不杀+log）；`EvaluateElimination` 签名+ctx 传播至 engine_position_close 消费面。
- `run.go` RunOnce 尾挂 runLifecycle；`wire_oppmatch.go` 装 dciexec executor 同拍调 AutoExecLive。
- `IsStrategyDisabled`/`ListDisabledStrategies` 投影=fin_strategy 非 live OR strategy_stats.disabled 并集双保险。
- 迁移回填：stats.disabled→retired、enabled→live（部署即保产——盲置 candidate 会切断在跑者）。

### 关键决策
- D-260 放前门：auto-exec 三面边界钉死（白名单+viable 卡+既有约束闸+投委会翻 live 才开火）。
- D-261 审计修正：`err.Error()=="no rows"`×2→`errors.Is(pgx.ErrNoRows)`（allow_rules.go 惯例对齐）。

### 验证
- ✅ build/vet/gofmt/触包测试/race/全套件 64 包零 FAIL（D587 flake 本轮未发）。
- ✅ SG 部署：迁移 1009 applied 04:23:59；`fin_strategy` 16 行=7 stats（1 live+6 retired）∪9 模板 candidate。
- ✅ SLA 首拍即火：5 条存量 proposed（#1/#3/#4/#5/#121）→expired+critical 告警；proposed→终态<24h 结构性保证。
- ✅ autoexec 全 candidate→n=0 静默（fail-closed 实证）；backfill 不径 SetStrategyState→零 live 告警（正确）。
- ✅ 判据种子与原字面量逐项语义对等；demotion 空表=安全侧（存面就绪消费件未建）。
- ✅ `fin_opp_eval` 5 卡管健。

### 状态/下一步
- ✅ M4.2 闭环（STATE ✅/D-261/master `c03609f3`）。下一票=M4.3 风控单入口（派单序 M4.0→M4.1→M4.2→M4.3→…）。
- 开放面：demotion 判据+消费件待委员会定值另票；paper 写径待委员会/probe 域；fin_strategy 键空间碰撞=理论面已记；`go N` 手工批准面保留（投委会权位）。

## 2026-10-12 切流后热修①：总览持仓闪动+「连接中」卡死（f2064f28）

### 业主报症
前端一直「连接中」；持仓列表 510/498 等行时有、时无，页面一直跳动。

### 取证（三通道交叉）
- `positions` 表：14 行全 CLOSED/FAILED、零活跃；`ListPositions` 连采 8 次 14/14 行集恒定——**拉路径零抖动**
- 隧道带权 `StreamUpdates` 60s 实测 122KB 帧稳定（尾帧即 `dataRefresh{funds}`），服务端无超时杀流
- 定位双引擎：`streamInitPosMonDomains` 每次（重）连回放 latestPos **全量 14 行**；`NotifyFunds`（账户资金三源写）≥2s 节流推 `dataRefresh{funds}`→ overview `load()` 重拉 ACTIVE=0 行整体替换

### 根因（全在前端，后端权威无损）
1. `overview.tsx` `onCase("position")` 未判状态直 upsert——回放/推送的 CLOSED 行混进 ACTIVE 域 map，下次 load 抹除=「时有」↔「时无」（同型真 bug：OPEN→CLOSED 行永挂活跃表）
2. `stream.ts` `onStatus` 注册不回报当前态+首帧前 `emit(true)` 早于父级监听注册（React 子 effect 先行）→ 指示永卡「连接中」

### 修法
- overview：position 事件按 ACTIVE 谓词（OPEN/OPENING/LEGGED/CLOSING）判定——活跃 set、非活跃 delete
- stream：跟踪 `currentStatus`、onStatus 注册即回报、`emit(true)` 挪至首帧到达
- 部署：`docker build --network=host`→save/load→compose up（embed web/dist 无独立径）；复验容器 healthy、RPC 14 行

### 遗留
- 若用户端→CF edge 链路（CN 段）仍 flap，回放仍在但每次回放被 ACTIVE 谓词正确过滤——行不再闪；指示届时诚实反映真实态而非卡死

## 2026-10-12 ARB 残骸清除（业主令「arb端已归档，SG不再需要arb项目」）

### SG 实清
- 停禁删 `cloudflared.service`（旧隧道 token 直嵌、与 nox 服务双跑——arb 域名 200 即其所喂；域名已归 nox 隧道）+ `cex-proxy.service`（业主令生产不落 proxy）+ `rm -rf /opt/cex-proxy`
- `docker rm arb-core arb-postgres`；`docker volume rm arb_arb-pgdata`（归档 dump 已验 `dump complete` 尾标 + 数据已克隆入 nox 卷实跑）；`docker rmi arb-core:latest/d108r1`；`docker network rm arb_default`；`rm -rf /opt/arb`（含 .env 密钥副本——删=安全+）
- `nox-postgres-test` 卷换名：克隆 `arb_arb-pgdata-test`→`nox_nox-pgdata-test` 后按 compose 重建容器；集群内 `ALTER ROLE arb→nox`+`ALTER DATABASE arb_test→nox_test`（临时超管径，tmp_admin 已回收）
- **保留 `/opt/arb-backups`**（29 份历史滚动备份+age 件）——数据保险非项目件，清除需业主另令
- 终检：容器/卷/镜像/目录/服务五面 arb 计数清零；nox-core/nox-postgres/nox-postgres-test 全 healthy；`cloudflared-nox` active

### 本地
- `git remote rename github→arb-archive`（只留 `nox` 远端推送，归档仓不再更新）
- `filt*` 七个迁移过滤残远端（/tmp/arb-m2*）清除；/tmp/arb-* 全删；本地 docker 零 arb 容器

### 终检抓残（同轮清）
- `arb_default` 网络残留 → rm；`nox-postgres-test` 卷/角色/库改 nox 名
- **`bin/` 构建产物不随 deploy_sync**——切流后 `/opt/nox/bin/` 空致 xasset-monitor/universe-radar cron 断写 1h（自盯 critical 实捕）：SG 就地 `build_host_tools.sh` 重建三件，手动跑通写库，自盯 18s 后报「已恢复」
- `/etc/cloudflared/config.yml`=alfaqs.org 他项目件，未动；cron 活动行零 arb

## 2026-10-12 切流后热修②：analyst 只读链路断——NOX_AI_PG_DSN 值域坐标漏改

### 告警实症
自盯报 `query_ro 不可用：failed to connect to user=arb_ro database=arb ... lookup arb-postgres`——连接串三面全旧（role/host/db）。

### 根因
1002 迁移只改 app_secrets/app_config **键名**（ARB_*→NOX_*），键内 DSN **值**（`arb_ro:pw@arb-postgres:5432/arb`）不在改名域；且生产 `nox_ro` 由影子 1004 集群级预造=NOLOGIN 无密码，`arb_ro` 切流时被 DROP——坐标与凭据双断。

### 修法（SG 实拍）
1. 一次性 `tools/tmpfix`（跑完删）：全表扫 app_secrets（解密判）+app_config 值域——`NOX_AI_PG_DSN` 为**唯一** arb 残值；坐标重写 `nox_ro@nox-postgres:5432/nox`（密码段原样）
2. `tools/ro-rotate`（D-059 现成工具）：`ALTER ROLE nox_ro LOGIN PASSWORD`（补 1004 按设计留空的 LOGIN+密）+32B 新密+Seal 覆写+SELECT 1 自验
3. 验证：`getent nox-postgres`=172.20.0.2（容器网 DNS ✓）、`rolcanlogin=t`、ro-rotate VERIFY OK；`AIReadOnlyDSN`/探针均热读 app_secrets——**免重启**，下轮自盯自绿

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

---

## 2026-10-13 · 名册离线假象修复（切流后热修③）

### 病灶
`fin_employee_registry` 出现「在岗显离线假象」：`wireEmployeeRoster`（装 Beat/Exit 钩）原装配于 `buildCexExchangeConfig` **之后**，而 `Beat` 仅在 `GoLoop` fn 启动打一次——`main:watchlist-provider`（钩前启动）首拍打进 nil 钩永久丢失，名册 `beat_ts` 恒 NULL 显「离线」。生产实证 `running=f/beat NULL` 与其代码中无条件 `GoLoop` 互证。

### 修复+验证（commit `a7fdfd73`）
`refreshTopic`+`wireEmployeeRoster` 提前至首个 worker 装配点前。SG 部署后实拍：`main:watchlist-provider` `running=t`+beat 新鲜；全册 123/142 岗本轮拿到心跳、110 running。

### 离册岗终分类
- **名册孤儿**（裁汰件历史行，代码已不接线）：`cmc-ws:kickHistory/seedFallback`、`main:intra-*`、`price_alert*`、`feishu_query:handle`、`margin_guard` 旧格式行——登记表 append-only 不回收
- **条件/事件岗**（注册但未触发启动，正常）：`probe-report`（探针平仓才跑）、`setup:mt5-quote-fwd`（env 装配路径）、`main:cexProxyHealth`（无 PROXY_URL 不启，M3 直连令后恒停）
- **一次性退出**：`main:trxvote`=`safego.Go`（非 Loop）`sched.Run` 正常返回 exits=36
- 仍存疑点：`margin_guard:okx|unified` exits=23640（ARB 期累计重启计数）——量级异常另票查

### 备注
修复首次写入被并行会话 `wip/ci-conn-retry` 的 `git reset`+checkout 冲掉（部署树被作开发树用的副作用）——改走 `/opt/nox-wt/roster-beat` worktree 提交、自 `/opt/nox` 推 master 指针过闸。

## 2026-10-13 部门日报 1301 根因修复（`1b81b792` 已部署实证）

**病灶**（07:05 BJ 首班：数据部+manager 两挂，10/11 过）——上游网关内容安全闸 code 1301，三通道定位（probe1301 二分法 SG 复现）：
- **硬触发**：「币安行情」CJK 短语（数据部名册 `main:binance-mkt-ws` role 独中——全库唯一 CJK 交易所名）
- **软触发**：「疑似冻结报价」+具体 USD 金额行并置（研究L6×账本L5/L7——单方各自全过，资金冻结诈骗语境误伤，概率判分边界抖动：同文前后两次一挂一过）
- **量触发**：十部全文 ~4.5KB 同源告警簇复述累计越线（重试 4 连挂、行级 dedup 仍挂）

**修复三件套**（deptreport 内，DB 原值零改）：
1. `scrub` 入文/出文脱敏：`币安→binance`、`冻结→停滞`（新触发词按同径补映射）
2. `compact` manager 输入单部正文 ≤300 runes 头 2/3+尾 1/3 行界缩量（保①状态与③待办）
3. `callLLM` 单重试（边界抖动重发即过）+系统提示补 venue 英文原词纪律（防输出侧自发译名）
fail-open 不变。**实证**：SG Generate 补班 11/11 全成+manager 行落库（1048B）+alert 留痕到达+全表零触发词——D-244 遗留③「完整日频周期」就此闭环。明早 07:05 正常班自动续跑（飞书扇出径未变）。

探针 tools/probe1301 用完即删（一次性诊断件未入库）。
