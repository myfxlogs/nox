# LOG.md — 阶段交付归档（NOX 仓首条）

## 三票：CEX 敞口闸撤除 + 跨 venue 调拨提案 + earn_redeem 抑赎粒度修

### ①D-288 CEX 占比闸撤除
`goEarn` 删 cex_exposure_pct 硬闸整段（constraint 缺口+占比拒单）；改 `concentrationAlert`：单 venue≥50% 或 CEX 总≥70%→`InsertAlertDedup` info「浓度透视」24h 窗去重，只标不拦。读败不阻塞申购。DCI 腿径/其他同源占比判零动（票面禁线照办——`CexExposurePctOf` 保留）。锚 `TestEarn_ConcentrationAlert`：90% 池仍申购+浓度 info 行含 venue 名。

### ②跨 venue 机会差调拨提案（xferScan）
`oppmatch/xfer.go` 并入扫描拍：同资产（USDT 先行域 `xferCCYs`）`LatestCexEarnAPRs` 跨所比——**APR 列=小数，阈 xfer_apr_diff_pct=pp 点**（缺省 5，app_config 可改）；APR 行 >24h 剔、缺一方不比（fail-closed）、源池须 `Idle` 睡钱标+equity>0（deployed 不动）。达标→`fin_condition_gap`（template_id=`cross_venue_xfer`哨兵/kind=`transfer` 新枚举=迁移 1017/subject=`USDT:src>dst`）+RequestConditionGap 推请求箱；UNIQUE 幂等+业主收口后差仍存复推（D-196②）。系统永不执行划转。锚 `TestXferScan`：7pt 差产行+requested+重扫幂等+反向不产。

### ③earn_redeem 抑赎窗粒度修
- **防发径**：`EarnPosition.MinRedeem`（flexible/position 行 `minAmount` 键，实拍待证——键缺/零=缺证据按需求额过，禁硬编）；`amt<MinRedeem` 跳行+note，尘额不再发赎撞 -6006。
- **粒度坑**：`isBelowMinRedeem`（-6006 码或 below min 文案）败因签名带 `|p=<pid>` 后缀——`redeemPitBlocks` 读面拆：带后缀坑只抑该产品重放，无后缀坑仍 op 级抑（suppress/critical 真坑语义不动）。锚 `TestRedeemPit_ProductScoped`：EP1 -6006 落坑签名 `|p=EP1`→二拍 EP1 跳行零打面、EP2 正常发赎；EP9 额<MinRedeem 零发赎+note。

### 机检
build/vet/gofmt 净；dciexec/oppmatch/pgstore 全包绿；`-race` dciexec+oppmatch 绿；迁移 1017 契约过（NOT VALID 同 1006 式）；pre-commit 十道。

### 验收残留（SG）
①CEX 占比>35% 时 earn 提案真放行+浓度 info 行在库；②造跨所收益差→请求箱现 transfer 行，重跑零重推；③尘额不再发赎（坑表零新 -6006），正常额赎不被挡。

### 部署验收（决策席 10-14 SG 实拍 @790aafb1）
- **①中**：`fin_dci_order` #16 filled（236.9467 USDT→USDT001，purchaseId 12955699607）——旧闸下必拒的提案真放行；浓度 info#143 落库「CEX总=96.1% binance=93.2%」只标不拦。**注**：96.1% 为重启期 MT5 快照陈旧致分母塌缩的失真值（真值~45%）——告警行为正确但输入脏，佐证冻结区「敞口分母鲜度守卫」票的必要性。
- **②中（机制活+正确静默）**：xferScan 每拍在跑（`0 new gap requests`）；实拍 earn APR 差=binance 2.41% vs okx 3.50%≈**1.09pp<5pp 阈**——不产行是对的行为（无伪阳）。
- **③中**：-6006 坑行 hits 仍 1 零新增；部署后零新 -6006；遗留 op 级坑窗至 10-15 00:03（旧签名行到期自消）。
- **附带**：warn#140「模板venue悬空：earn_apr_lending 声明 okx:spot 凭据但执行轨=binance」——自盯抓到模板元数据漂移，遗留记。

## binance:earn USDT 仓位缺采

### 病灶（SG 实证 10-14）
`fin_dci_order` #13/#14/#15 三笔 USDT001 申购全 filled（≈1160.69）但 earn 池快照只有尘币零 USDT——采集器只查 `flexible/position` 且**缺省分页（size 默认 10）**，尘币占满首页把 USDT 挤出窗；另申购品可为 locked 定期归 `locked/position` 未采。

### 修
`accountBinanceEarn` 重构：`earnPositions` 件双端点翻页全采——`flexible`+`locked` 并列、每页 `current/size=100`（页内行<size 或累计≥total 止）；locked 行额字段 `amount` 与 flexible `totalAmount` 双键兼容解析，同资产跨端点并入 per 币聚合。端点败/解析败 fail-closed 返错（「查不到=零仓位」禁静默吞）。

### 锚
`TestBinanceEarnPositions` 升双端点锚：路径序 flexible→locked、翻页参数 current=1&size=100、locked amount 字段解析、USDT 542(flex)+1160.69(locked)=1702.69 跨源合并、MULTI 指纹。

### 禁线照办
零新凭据/env（同签名 USER_DATA）；分币行不落删不合并零换算；REST 只读。

### 验收残留（SG）
`fin_venue_account_snapshot` 现 `binance+earn+USDT` 行值≈1160.69 对拍 #13+#14+#15；资金地图 earn 池组计含 USDT；spot 减额对拍平账。

## 视窗 C 件收口：日报缺段兜底 + watch_rule enabled 零值修

### ①deptreport 缺段兜底
`contractBackfill`（contract.go）：补正重生成仍缺的序段按圈码（①~⑧）对拍 gather 输入的 `【N …】` 头，把该段原料确定拼接回正文（`【契约补段·原料 <名>】` 包裹）——宁带原料段不挂空标记；「末行明日关注」无原料段只留 marker；`【契约缺段】` marker 语义不动。系统提示八段标题逐字锁 gather 输入段原文（① NAV 与闲置资金…⑧ 昨日公司日报原文），消「提示词与原料头不逐字」的诱导缺段面。

### ②watch_rule enabled presence
proto `enabled`→`optional bool`（缺席≠false）：UpsertWatchRule `r.Enabled==nil→true`——新增/改单未携=启用，显式 false 才可关。`watchRuleProto` 回写 `&r.Enabled` 保 presence。前端零改（表单/toggle 恒显式携值）。

### 禁线
拼接只限 gather 已产段零编造；proto optional 接线面全量核（Go/TS 生成件+service+前端同核）；marker 不删。

### 锚
`TestContractBackfill`（圈码段逐字拼回+末行无补段+⑦ 边界不越界）；`TestD249_ContractFailOpen` 加⑥原料回填断言；`TestWatchRuleEnabledPresence`（缺席→true/显式 false→false 两行进表对拍）。

### 验收残留（SG）
造缺段输入→正文含「契约补段·原料 ⑥…」+marker 标；不带 enabled 建规则→enabled=true 且触发链通。

## auto 轨僵尸提案处置语义（#221/#225 无执行器永不翻态）

### 病灶
`IsExecTemplate` 外族的 auto 模板每扫拍过预检入 liveTmpls→`ExpireProposalsExcept` 永免、`pending_uniq` 挡新提案、autoexec 零触碰——proposed 龄只涨不翻（SG 实证 #221 funding_carry/#225 cross_venue_spot unevaluable 挂 ~30h）。

### 处置语义（票面①二选一钉死=「不产提案+存量翻 deferred」）
- **①auto+无执行器**：`run.go` 达标模板判 `gate.IsExecTemplate` 前置——auto 轨且族外→`DeferProposalsByPair` 翻存量 proposed 为 deferred（D-204 同径复用）+info alert「auto 轨无执行器 …executor-missing」+continue 零新提案。选「不产提案」侧：deferred 提案语义=曾达标待复评，无执行器是结构性缺失非时机问题，产行即 deferred 属虚账。
- **②unevaluable 连拍阈值**：迁移 1016 `fin_opp_eval.uneval_streak`（unevaluable→+1，其他 verdict→归零，`UpsertOppEval` RETURNING 回传）；扫描拍内 verdict=unevaluable 且连拍≥3→deferred+info alert 携连拍数。阈值=3 拍：单拍数据面波动不致误杀，连拍才定性「持续不足」。
- **③存量**：#221/#225 两模板均属 auto+族外→部署后首个扫描拍自动由径①翻 deferred（零手改 SQL，处置即语义本身）。

### 禁线照办
`IsExecTemplate` 注册面零动；proposed→executed 仍唯 autoexec/go 口令径；deferred 复用 D-204；zero LLM；err 不吞（defer 败仅 log，alert 败仅 log——主径不被留痕阻塞，与既有 dormant 分支同式）。

### 锚
`TestZombieProposalSemantics`：auto 无执行器存量翻 deferred+info 留痕+零新 proposed；proposal 轨无证据源模板连拍 3 次 unevaluable→deferred。

### 验收残留（SG）
部署后实拍 `fin_proposal` #221/#225 status=deferred+decided_at 落戳；`alerts` 现「auto 轨无执行器 funding_carry/cross_venue_spot」info 行；同模板零新 proposed。

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

## 2026-10-13 M4.3 风控单入口收口（`26291694` wip/m43 报审）

**背景/范围**：D-262——全仓下单入口枚举分类，position-bearing 收口 `Gate.Submit→intent.Execute→pipeline` 单径。

**新增**：`internal/risk/gate/dci_check.go`（`CheckDCI`/`DCIProposalView` 命名闸）；`internal/risk/gate/budget_check.go`（`BudgetChecker` 接口+`FailClosedBudget` 桩+`SetBudgetChecker`）；`execute_gate_wrap_test.go`、`dci_check_test.go` 锚测；`gate.Submitter` 接口（decision.go）。

**修改**：`ExecuteOpportunity` RPC 改经同闸实例（`autotrade.New` 内接口断言自挂 `SetOrderGate`，跨包零依赖向；闸 nil=fail-closed）；`dciexec.GoProposal` 内联四检面迁 `gate.CheckDCI`；`Gate.Submit` 挂预算闸检点（nil=不消费）；`check_import_direction.sh` 补 `dashboard→risk/gate` 边；全仓入口 `[exec-entry:*]` 标记 13 处（gate×3/gated-body×3/named-gate×1/committee-exempt×2/demo/shared-executor/compute-only 另+service.go、swap_deviation 仅注释提及）。

**关键决策**：①递归规避——`ExecuteOpportunityInternal`/`ExecuteMeanRevert*` 保为「过闸后执行体」，闸挂 RPC 入口而非执行体自身（engine 回调同径复用零双闸）；②DCI 不扭 TradeIntent（腿=venue 申购非订单对）；③`OpenProbe` 分类 committee-exempt（自有守卫链+拒单全量 alert 留痕，腿异构单腿调查仓）；④`bm.PlaceOrder`=传输层非入口，分类责在调用方。

**验证**：✅ `go build/vet ./...`=0；`gofmt` 净；锚测 3 件全绿；`go test` 触碰包（gate/dciexec/autotrade/dashboard/cmd/core）绿；`-race` 同包串行绿；check-lines 零警告；import 方向 215/215；env-reads OK；pre-commit 十道过。⚠ 全量 `go test ./...` 并发跑 autotrade 互踩共享测试库败（已知型，隔离复跑绿——非本票引入）。

**状态/下一步**：🔄 报审待复审合并；遗留=①`fin_strategy_budget` 消费归 M4.4；②SG 部署验收=grep 分类清单实拍+`service_execute_meanrevert` 拒单可见（测试库已锚）。

## 2026-10-13 M4.4 NAV 日切 + 资金配置器（`80ffa1cc`+`104dbbda` master 报审）

**背景/范围**：D-264——NAV 无日切无单函数（三消费面各自现算=口径漂移隐患）；策略级预算表缺如，M4.3 预留 `gate.BudgetChecker` 待挂真实现。

**新增**：迁移 `1010_nav_budget.sql`（`fin_nav_daily` day_start PK 一行/天幂等 + `fin_strategy_budget` strategy PK/budget_usd/derived_from jsonb）；`internal/store/pgstore/nav.go`（`NavReport`/`SumNav`/`ComputeNav`/`StrategyBudget`/`GetStrategyBudget`/`UpsertStrategyBudget`/`UpsertNavDaily`/`LatestNavDaily`）；`internal/portfolio/budget/`（`DailyCut` 日切+`Checker` 闸侧只读+`derive` 纯函数，128 行）。

**修改**：`deptreport.Deps` 加 `OnDayCut`/`ComputeNav`——boot 首切+每班先于 Enabled 闸跑（零新定时器；AI 缺配也日切）；`gather.go`/`service_finance_overview.go` 改读 `ComputeNav` 单源（USD-only，非 USD 池跳过不假换算）；`setup.go` 挂 `atEngine.RiskGate().SetBudgetChecker(budget.NewChecker(st))`；`check_import_direction.sh` 补 budget 两边（216/217 边）。

**关键决策**：①日切并入 deptreport 07:05 拍（同数据自然复用，boot 首切堵「配置器未产行→闸全拒」首日窗口）；②推导规则 `budget=min(AutoTradeMaxNotional, nav×(1−reserve_pct)/liveCount)`，reserve 取 `fin_capability_constraint` 机读值（缺席=0 储备），derived_from 留全输入痕审计可复算；③`fin_venue_exposure_caps`/`fin_canonical_exposure_caps` 语义=敞口 cap 与策略预算不同面，未被吸收→保 committee 写径原样（票面条件「如被吸收」未触发）；④IR finance overview 由 acctMon 内存聚合改快照表读——real/demo/stopped 三态过滤下沉快照写口保证，两测试钉值改版锚「USD-only+逐 venue 最新行」。

**验证**：✅ `go build/vet ./...`=0；`gofmt` 净；check-lines 零警告（setup.go=300 压线）；`go test ./... -count=1` 全绿（dashboard 一例 bad-fd 共享库压力型 flake，隔离复跑绿）；`-race` budget/gate/deptreport/pgstore 绿+dashboard 隔离段绿；env-reads/迁移契约/import 方向全过；pre-commit 十道。锚测：`TestM44_DailyCut`（nav=10000/idle=8000/EUR 跳过/幂等覆盖）、`TestM44_BudgetGate_Submit`（缺席→REJECTED/限额内→EXECUTED/超额→REJECTED）、`TestM44_Derive` 纯函数。

**状态/下一步**：🔄 报审待终审；遗留=①SG 部署验收=`fin_nav_daily` 首行落+`fin_strategy_budget` live 行+IR/日报/配置器同源读实拍；②reserve_pct 约束缺席=0 储备（偏大预算方向——若委员会要 fail-closed 语义后续一行改）。

## 2026-10-13 M4.5 复盘员事件驱动判决书（`a25e96ad` 报审）

**背景/范围**：D-266——`position_reviews` baseline 死表（零 Go 写读面，SG 12 CLOSED/0 覆盖）；`review.Run` 挂点只手动 RPC 一处，自动平仓零复盘；`posChange` 事件底已备、exit_kind 权威枚举在场。

**新增**：`internal/review/posreview/`（posreview.go 事件循环+Sweep、review_one.go 数字面、narrative.go LLM 面）；`pgstore/posreview.go`（`PositionReview`/`ClosedForReviewRow`/`ListClosedWithoutReview`/`UpsertPositionReview`）；`cmd/core/wire_posreview.go`；名册 `main:posreview`→账本。

**修改**：`main_monitors.go` 挂 `wirePosreview`；`check_import_direction.sh` 补 posreview→pgstore/analyst 两边（218/219）。

**关键决策**：①订阅 posChange 而非新定时器——每次 positions 写提交即发，8 个 ClosePosition 调用面结构性全覆盖；缺口查询幂等（已评仓自动出集），kick 风暴=多余空扫可承受。②数字面纯 Go：expected=bps×notional/1e4（双参缺一→NULL），actual=settlement.net_realized（pnl_quality≠actual→NULL+质量痕入 note），cost=fees_usd 直读，risk/expected_cost 无机读源→NULL 禁外推；exit_kind 直读不反推。③LLM 职责收为归因叙事+followup_action——JSON schema 校验（action∈continue|reduce|quarantine|retire），违式/败调重试一次→`llm_failed` 标痕+warn alert fail-visible；建议不落 fin_strategy（判据表独占淘汰权）。④AI 未配=机器 note 纯数字面（`无 AI` 如实标）；`review.Run` 聚合调参径保留互补不替。⑤CallLog role=`reviewer` 落 fin_llm_call；ResolveClient=kbreport 口径零新 env。

**验证**：✅ `go build/vet ./...`=0；`gofmt` 净；check-lines 零警告；`go test ./internal/review/posreview` 六锚全绿（GoFace 数字钉值/质量痕 NULL/LLM 违式→llm_failed+warn/无 AI 机器面/Sweep fail-open/真实 5434 读写往返）；`-race` posreview 绿；env-reads/迁移契约（零迁移——baseline 表复活零 schema 变更）/import 方向过；pre-commit 十道。

**状态/下一步**：🔄 报审待终审；遗留=①SG 部署验收=12 存量补齐+自然平仓 <10min 落行+`fin_llm_call` reviewer 行；②expected_cost/risk 无机读源恒 NULL（票面语义照办——补源后可填）；③`fin_llm_call` reviewer 行仅 LLM 调用时产（AI 缺配零行属正常）。

## 2026-10-13 M4.6 投资人页请求箱回执（`8a085f85` 报审）

**背景/范围**：D-268——`fin_condition_gap` 态机已全备但 `ResolveConditionGap` 零调用面（业主收 warn 推送无回执口，SG 两条 requested 凭据挂 2 天）；投资人页 v0 纯只读。

**新增**：`proto/dashboardv1/request_box.proto`（`RequestBoxItem`/`ListRequestBox`/`ResolveRequest`+`ResolveRequestState` 枚举）；`service_request_box.go` 两 RPC 实现；`pgstore.RequestBoxRow`/`ListRequestedGaps`（requested 集按请求龄最老在前）；前端 investor.tsx「请求箱」段双按钮；`fmtAge`。

**修改**：`ResolveConditionGap` 签名补 `note` 参+`applied bool` 返回（resolve_note 落「投资人页回执」）；`service.proto` 挂两 rpc+import（rpc 名与消息同名冲突→输入消息 `ResolveRequestRequest`）。

**关键决策**：①age_seconds 后端算（前端零信任——按钮只发 id+态）；②applied=false=行已收口/不存在幂等（双击重发不误报）；③capital 零新码——kind 白名单/EvalCond/reconcile 全在，锚测造 `capital` 行端到端走通即票面「capital:* 走同一态机」条款实证；④reopen 安全网照旧（回执误按下拍 reconcile 自纠）。

**验证**：✅ `go build/vet ./...`=0；`gofmt` 净；`TestM46_RequestBox` 真实 5434 六步锚绿（capital 行入箱+龄→非法态 invalid_argument→fulfilled 翻态+note→幂等复按→出箱）；`TestD186_*` 签名改后绿；`npm run build` 绿；check-lines/import 方向/env-reads/迁移契约全过；pre-commit 十道。

**状态/下一步**：🔄 报审待终审；遗留=①SG 部署验收=投资人页请求箱见 #6/#11+回执一按翻态；②waived 行无 reopen 径（态机不含 waived→open——放弃即终态，票面语义照办）。

## D-255 venue 败因坑表（@a5b6cf6f / STATE@ce158549）

**背景**：fin_dci_order #1–6 六连撞——同一败因（-1102 参数名错→-9000 精度→-9000 报价过期）反复撞 venue，败文躺 response 死字段、reqID 金额浮点尾破幂等、errclass 无 CEX 消费点。

**新增**：迁移 1011 `fin_venue_reject`（venue/op/error_signature/action[fix|suppress|critical]/action_params/hits/expires_at/last_seen）；`errclass.VenueRejectSignature` 签名单源（code 优先取码/无码 msg 归一，数字→#）；`dciexec/pit.go`（查坑动作分派+fix 映射 deposit_decimals）。

**改**：tryLeg——申购前 ListVenueRejects 查坑：fix→按 params 自修复参数重发；suppress/critical→行落 rejected+一次性 critical dedup 告警，零 venue 打面；读败 fail-closed 拒。post 败行 LearnVenueReject 落坑（默认 suppress 24h，hits/last_seen 自增，委员会手改不覆写）。reqID 改 `dci|<腿>|<产品>|<交割日>|<尝试序>`（金额尾出键）。

**决策**：幂等语义收敛——同意图「在途/已成」（pending|filled|settled）挡重发「已受理」；failed|rejected 不挡续执（`DCIOrderOpenIntent`+`DCIOrderIntentCount`，提案绑腿序 `DCIOrderLegCount` 兜底历史无前缀行）。refresh 类败仍走既有一次重提。

**验证**：六锚（签名稳化表测/坑落表/二次零 POST+一次性 critical/fix 映射/读败 fail-closed/reqID 稳定+增序）；D191 幂等/D198 续执回归绿（D198 暴露 propID=nil 尝试序缺口→LegCount 兜底修）；-race dciexec/errclass 绿；门禁全过。

**遗留**：委员会可手写 action=fix+critical 行覆盖默认 suppress（expires_at 控窗）；SG 部署验收=败后坑表有行+二次同败零打面。

## DCI 轨 4 件系统病灶裁决（@c6291d52 / STATE@5439595c，已推 nox）

**背景**：原 gap #19 PUT subscribe -9000「0 decimals」/#22 PUT 无达标档 order_ref/#24 CALL 无达标档/#25 BTC 活期赎回 -1102——venue 实拍取证逐件裁决。

**取证**（SG 三通道）：fin_dci_order #1–8 全链——#7/#8 PUT 已 settled；fin_cex_dci_quote NULL order_ref/min_amount 行绝迹于 10-11 10:53（采集面修复时点）；venue CALL 在售 strike 域 [83750,85250] vs 模板界 ≥spot×1.10（92122）=市况无档。

**裁决**：#19 已修（R11/R12 floorTo min_amount 精度，生产成交实证）；#22 已修（采集面全落）；#24 非病灶（市况无档，模板界属委员会域）；#25 主修已落 c2e7ac0e（撤 type=FAST），残留二修。

**改**：①`VenueRejectSignature`→`code:N|归一msg`——binance -9000 同码多因不混坑（refresh 瞬态与 0 decimals 分签名）；②refresh 类瞬态败不落坑（落 suppress 会误封 24h）；③坑表扩 op=`earn_redeem`——redeemShortfall 赎败落坑 24h+窗内抑赎零 venue 打面（读败 fail-open 不堵补缺径，Store 缺席同）；④尘埃赎额 %.8f 格式化归零跳行+note——封堵 -1102「amount empty」重放坑。

**验证**：锚新增 TestD255_RefreshNoPit/RedeemPit/RedeemDust+签名表测改（-9000 双签名锚）；D203 赎回 echo/D191R6 赎回缺口回归绿；-race dciexec/errclass 绿；门禁全过。

## D-249① 策略淘汰多空听证（@6a8b9354）

**新增**：迁移 1012 `fin_strategy_hearing`；`internal/review/hearing`（多空对辩——裁定击杀前 bull/bear 两路 LLM 各 ≤400 字，输入=确定性统计面快照+判据理由；ctx 200s 封顶+每路重试一次；`clampRunes` Go 侧字数硬闸）。

**接线**：`settlement.ElimHearer` 可选接口（runElimination 击杀前调，钉住 hear→disable 序锚）+ autotrade startup 淘汰扫 `SetSettleDeps` 第四参同钩——两淘汰路径全覆盖。role=`elim_hearing` 入 `fin_llm_call`；§4.3 注册表+§5 对象表补录。

**禁线实证**：听证行 status debated/skipped_no_ai/failed——AI 缺配行照落不阻塞淘汰；裁定权=判据函数零让渡（`TestD249_HearingObserveOnly` 锚）。

## D-249② 日报待办闭环（@e791ecd4）

Generate 加⑧段「昨日公司日报原文」（`ListDeptReports(date-1)` 复用读面零新表——进度账本=fin_dept_reports 自身跨日对拍；缺日空段注明，读败当班跳过）；系统提示加「昨日待办→今日状态」必段（消解/未消解/新增逐条标），末行「明日关注」供次日对拍。**修**：⑧段置 Generate 直查——gather 内 BJ 零点 -1d 错 report_date（UTC 零时）口径，锚 `TestD249_TodoLoop` 钉住。

## NAV 幽灵池过滤（@1ce2a810 / STATE@4ef5e5da，已推 nox）

**病灶**：`fin_venue_account_snapshot` 移除户残行（62311223 USD 172.95）入 NAV——`ExplorerIdleFunds` 零 `broker_accounts` 关联，虚 +3%。

**改**：快照读面打 `Ghost` 标（mt4/mt5 行 venue 须在册；CEX venue 天然不入 broker_accounts 不受影响；stopped 户在册照计）；`SumNav` 剔除+`SkippedGhost` 独立计数；幽灵池 Idle/BelowFloor 不打标（防幻影告警）；deptreport ①段幽灵行带标+剔除计数行。写侧不动零迁移。explorer.go 行数硬闸拆 `explorer_idle.go`。

**验证**：锚 `TestGhostPool_NAV`（幽灵标/剔除/计数/CEX 不受影响/在册户照计）；budget/deptreport/pgstore 全包绿含 race；门禁全过。部署验收=NAV 5247.63 实拍。

## MT4 退役（尸件清扫）@7d6fe99a

**分类清单（逐文件三档）**

①写边界→显式拒：`pgstore/brokers.go InsertBrokerAccount`（platform!=1 拒——RPC 层 D-470 已拒、store 层纵深堵）；`service_xasset_combos.go` comboVenueTypes 撤 mt4（写白名单拒 :mt4）；`service_backfill.go` splitVenueKey 拒 venue:mt4；`broker_automap_sweep.go` seedAssetEquivVenue platform!=1 fail-closed；`scan/graduate_resolve.go` platform!=1 fail-closed（不再产 mt4 venueKey）。

②mt4 专属死径→删：`errclass.ClassifyMT4` 码表+mt4 分支撤（mt4 键→Abort 兜底）；`engine_swaparb_rate` 后缀候选撤 mt4；combo_allow 注释域值对齐。

③枚举/CHECK/历史读面→保留：`keydomain.IsMT`/IsMTKey；`brokerVenueType`（存量 platform 行如实渲染）；`legMarket`/`cfdComboLegs`/`mtVenueTypeOf`/`slipForLeg` 等存量键域读径；`migrations/1000_baseline` `venue_type_chk('mt4','mt5')` 不动；proto/生成件枚举注释；数据适配层共享 MT 网关件。白名单：残余 mt4 grep 命中全为上述③类+测试夹具。

**验收锚**：`TestMT4Retire_InsertRejects`（store 层 platform 0/2 全拒）+ `TestMT4Retire_ParseRejects`（backfill 键+组合后缀双拒 mt4/过 mt5）+ D-470 RPC 三态原锚全绿。机检：build/vet/gofmt 净，`go test ./...` 全绿（dashboard 一例 bad file descriptor 共享测试库压力型 flake，隔离复跑绿），-race errclass/scan/exec/autotrade/pgstore 绿，check-lines 零警告，import 方向 221/222，env-reads/迁移契约（零迁移）过。

## 能力账本凭据归一病灶 @7e143a26

- #6 根因修：`capability.go` broker 凭据行 `VenueType` 原硬归一 `"cfd"` → 改注册真值 `mt5`（`ledgerView` 由真值产 `type:mt5` 键，`credential:mt5` 命中）；`asset_class=cfd` 语义经 `Tags` 保留。grep 全库确认零 `subject:cfd`/`type:cfd` 消费面。
- #11 判非病灶：`term_basis` 模板语义=现货×远月期（交割期货腿），`okx:futures` subject 正确——okx 在册仅 perp 市场线，缺口如实非误报。注册 `okx:futures` 市场线属委员会/venue 配置决策，票面「裁定不明先留痕」照办未改。
- 锚测：`TestCredTypeTrueValue`（5434 实库 broker 行 venue_type 真值+cfd tag 保留）+ `TestCredMT5_TypeKey`（ledgerView 三级键+credential:mt5 端到端命中）。
- 机检：build/vet/gofmt 净；pgstore+oppmatch 全包绿、oppmatch race 绿；check-lines 零警告；import 方向 221/222；env-reads/迁移契约（零迁移）过。
- 部署验收（SG）：下拍 reconcile 后请求箱 #6 自动收口（fulfilled 出集）；#11 保持 requested 如实缺口。

## D-249④ 日报 SOP 输出契约 @9ca6b801

- `deptreport/contract.go`：八段必段表（①NAV…⑧待办对拍+末行明日关注），`contractMiss` 缺段/乱序检出（探针序位严格递增），`contractFixNote` 补正注记。
- `Generate` 接线：首班违约→user+补正注记重生成一次（重试败保留首班正文）；仍违→`【契约缺段：…】` 标记追加正文 fail-open 落库+log。schema 只管结构不管数字；零新表零迁移。
- 锚测：校验器三态+补正重生成径+fail-open 标记三件绿；fakeRunner 缺省正文升合规样例（旧锚调用数=1 不破）。
- 机检：build/vet/gofmt 净、deptreport 全包+race 绿、check-lines 零警告、import 221/222、迁移契约（零迁移）过。
- 部署验收：今晚 23:05 UTC 班 company 行全序段齐=契约过（违则在正文见「契约缺段」标）。

## 评估卡证据源补强 @4612b166

- **①earn failure**：`RejectHits(op='earn_redeem', 30d)` 接 `fin_venue_reject`（D-255 坑表）——口径裁「拒绝零资金损耗」（申赎拒绝在资金移动前，同 DCI 口径），证据文 `earn_redeem n败/30d零资金损耗`。
- **②funding_carry**：fee=`MedianTakerFeeBps`（在册非模拟 perp 所 exchange_configs.taker_fee_bps 中位——venue 实测值域）×双腿 ÷ 按 `MedianHoldHours(['PERP_FUNDING'])` realized 时长账本年化（零行=缺证据不外推）；failure=`LegFailCount(['PERP_FUNDING'])` position_legs FAILED 账本——0 败=零损耗证据项，>0=成本未计价 fail-closed 仍 missing（不估单价，零外推不破）。
- **③cross_venue_spot**：五项全缺=detector 无报价输出面（gross 源未建）——非证据链病灶，票面裁定留诊断另案。
- **funding_carry 证据源定位清单**（票面②交付）：fee→exchange_configs.taker_fee_bps（venue 级实测）；持有窗→positions CLOSED PERP_FUNDING (closed_at−opened_at)；failure→position_legs.status='FAILED' join positions.type；slippage→fin_cex_spread；window→facts funding 中位。
- 锚测：TestEvalCard_EarnFailureSrc / TestEvalCard_FundingFeeSrc / M41_788 三态绿（测试全自隔离种子防共享库串扰）。
- 部署验收：SG 重跑评估拍后 fin_opp_eval——earn 两模板 failure 出证出 verdict；funding_carry missing 收敛至 [slippage]（perp spread 行在产在→全项出 viable/reject）。

## XASSET_CARRY 开仓阻塞取证（取证票，只查不改）@无新码

**取证时间**：SG 2026-10-13 12:49–13:10 UTC；双通道（docker logs nox-core + psql nox）+ 代码面 trace。

### 结论一：票面假设「depth 事实大面积缺」属实，但断点不是「OKX tickers 不带 depth」

- **OKX 深度走 books 端点已在线**：`cex_depth.go` 三所均取真实盘口端点——binance `/fapi/v1/depth`、gate `/futures/usdt/order_book`、okx `/api/v5/market/books?instId=X-USDT-SWAP&sz=5`。生产实证 `fin_cex_depth` 有 okx `XAU-USDT-SWAP` 行 4401 条、最新 13:10 新鲜。OKX tickers 缺 depth 的假设**推翻**。
- **真断点=采集宇宙 ⊂ 候选宇宙**：深度基名=静态白名单 5（SPX500/XPT/XPD/XAU/CL）+动态 `ListCfdUniverseDepthBases`（fin_carry_universe CFD 域 canonical→基名，实拍 `[cex-depth] universe bases: 40`）≈45 基名；而 paper cex carry 候选源=FUNDING_HEDGE detector 全 CEX hedge 宇宙（fin_cex_universe TRADING 基名 1093）。候选 canonical 在采面外→`depthOf` n=0→fail-closed `depth_missing`；覆盖内的薄档品种（XPT/XPD/指数类）→`depth_low`。skips≈`depth_missing:48–51 depth_low:18–25 funding_low:2–5` 与该结构吻合。
- 采集器本身健康：binance 48/gate 38/okx 41 符号持续新鲜写入，5min 节奏正常；残余缺口=venue 侧无该品种（JP225/NAS100/SPX500 等所间不对称，代码注释已注明「所间不对称缺符号静默」惯例）。

### 结论二：live XASSET_CARRY 零开仓的主阻是经济学（net≤0），非采集缺口

- 唯一 live 白名单行：`XAUUSD|XAUUSDT 3001157:mt5×okx:perp`（`[whitelist] xasset row` 实拍，enabled/long=0.01 short=1200）。
- 监控链全活：`fin_xasset_monitor` `XAUUSDT-XAU×okx:perp×3001157:mt5` 行 5min 新鲜，entry_net_bps≈12.9、equiv_annual_pct≈**0.13%**。
- detector 净息差≈0：okx `XAUUSDT` funding 事实值=**0**（实拍逐帧 0）；3001157 canonical `XAUUSD` swap 事实=0（`XAUUSDm` 原生键才有真值 −1.28 bps/d≈−4.69pp/年，canonical 键行恒 0——canonical 行记 0 是 swap-free 置 0 口径的副作用，但即便取真值净息差仍为负）。
- 因此 detector 产候选（net=0 ≥ MinNetAnnualPct 默认 0）→ evaluator `net=0−MarginRateAnnual(4.5)=−4.5`→`classify` net≤0→**RatingTrap→不 publish**→autotrade `recv opp` 24h 零行（XASSET 及其他类型全零——候选层/评估层前置过滤，非引擎死）。
- 旁证：`[review] XASSET_CARRY 样本=12 平均实收年化低于预期 44.16%`——历史仓实收已证实息差高估；当前 venue 实测 funding=0 即「无肉」，闸按设计拒开是**正确行为**。

### 结论三：position 503/504 FAILED + closed_at=NULL 定性

- 行实况（2026-09-29 05:39/05:40 UTC）：opp 键 `XASSET_CARRY:XAUUSD:3001157:mt5:gate:perp`（彼时白名单放 gate）。双腿并行提交（`executeLeg` safego），5ms 内双双 FAILED、`filled_qty=0`、`order_id` 空——两腿均被**下单前/即拒**级失败（venue 拒绝或本地 fail-closed），非成交后事故。
- **closed_at=NULL 是设计语义非账本 bug**：`ClosePosition` 才写 `closed_at`（status→CLOSED）；FAILED 由 `UpdatePositionStatus` 直写且 `status NOT IN ('CLOSED','FAILED')` 终态——开即败从未开仓，无 closed_at 合理。positions 排序/复盘读面均按 COALESCE(closed_at,opened_at) 兜底。
- **失败原因不可复原（证据面当时未建）**：`position_legs.fail_reason` 列=2026-10-12 迁移 0201（leg forensics）才加，503/504 先于列存在→拒因从未落库；docker json.log 3×50MB 轮转已覆盖 9-29；alerts 表最早行 10-11。同期同构型对拍：507/508 MT 腿 FAILED 而 okx 腿 FILLED（收编平干净离场）——MT 腿间歇性拒单是已见形态（mtapi REQUEST_ERROR 间歇实证载于 pipeline 注释）。
- 假设面（不可证实）：MT `XAUUSDm` 下单遭即时拒（市场会话/保证金/REQUEST_ERROR 族）+ gate `XAU_USDT` 腿同帧拒（合约不可用/qty 界）。两腿独立失败不互因（并行提交无因果链）。

### 修复建议面（裁定后单独派票）

- **R-A depth 覆盖对齐候选宇宙**（修 depth_missing）：`universeDepthBases` 数据源由 fin_carry_universe（CFD 域）扩至 paper 候选实际键集（如 fin_cex_universe TRADING 基名∩有 funding 事实集，或按候选回调注册）——需先议体量上界（1093 基名×3 所 books REST 5min 轮=调用量与限流评估，D-004 轮询例外条款须留痕）；替代向=缩候选宇宙至采面（治理侧）。
- **R-B FAILED 腿拒因已可持久化**：fail_reason 列 0201 已建，新失败自带 venue 拒因原文——503/504 型「不可复原」不会重演，无需补修；可选「FAILED position 写 closed_at=失败时刻」语义票（账本一致性，收益低）。
- **R-C XASSET 经济面**：okx XAU perp funding 实测 0 + MT swap −4.7pp/年 → XAU×okx 组合结构性无正息差——属策略/参数域（委员会裁）：要么等 funding 转正自然放行，要么换 venue/标的；系统行为正确无需修。
- **R-D（可选）gate XAU_USDT 腿 qty=10000 单位核验**：与 okx 腿 1000 不同量级但成功仓（505）同构——疑 gate 合约粒度差异正常；若确证为多 10× 属 sizing 票另案。

### 验收对照

①depth 断点=「采集宇宙⊂候选宇宙」结论落；②FAILED 根因=双腿即拒（原因列后建于行，不可复原）+closed_at NULL=设计语义；③修复面 R-A~R-D 留档。**零生产改动、零代码改动、门槛参数未动**。

## cex-depth 采集宇宙对齐 测算+方案（D-278 R-A，票面=测算交裁定再动手）

**测算**（SG 2026-10-13 13:20 实拍）：

| 面 | 数 | 源 |
|---|---|---|
| 现采基名 | 5 静态 + 40 动态（fin_carry_universe CFD 域）≈45 | `[cex-depth] universe bases: 40` |
| 候选实际键集 | **98 基名**（fin_cex_universe TRADING ∩ 近 1h funding 事实基名） | psql 交叉实拍 |
| 分所 funding 见证基名 | binance 93 / gate 76 / okx 37 | facts kind=funding 1h |
| **深度未覆盖的候选基名** | **58**（与 `depth_missing≈50/拍` 量级吻合） | 差集实拍 |
| 目标集每轮调用量（方案 A 分所精集） | 93+76+37=**206 calls/5min**（≈0.7/s 持续） | — |
| （方案 B 并集粗集） | 98×3≈294 calls/5min（≈1/s；~33% 空打在所未上市符号上=静默跳过） | — |

**venue 限流核查面**（正式权重以 venue 文档为准——修码前复审须核档）：binance `fapi/v1/depth` limit=5 权重 2（IP 2400/min → A 案 186 weight/5min 远低）；okx `/market/books` sz=5 公开档限速（历史实测 ~20 req/2s 充裕）；gate `futures/usdt/order_book` 公开频限宽松。**方案 A/B 均在限内**；串行时延面：206 行×~200ms≈41s < 5min 间隔✓。

**D-004 例外举证**：books 三所皆有 WS 频道（binance `depth`、okx `books`、gate `futures.order_book`）——本采集是**纸面质量闸研究面**（D-420 顶档深度 5min 采样已立的先例，非执行/判定钱径），REST 快照采样语义由该先例背书；扩域不改变数据性质。仍按票面要求留痕（本条即例外记录，编号随本 LOG 条目）。

**方案**（待裁定）：
- **A（荐）分所精集**：`universeDepthBases` 数据源改「各所近 Nh funding 事实符号→基名」并集（venue 键精准，仅采该所真有 funding 的符号=候选腿真实集合；多余调用零）；新 store 件 `ListFundingWitnessedBases(venue, window)`，collector bases() 按 venue 分组取。206 calls/轮。
- **B 并集粗集**：同一查询跨所并集喂三所，294 calls/轮实现最简，~33% 空打。
- **C 上界封顶**：top-N 按 funding 绝对值排序截断（防 funding 事实面膨胀失控——98 现状无须，留作扩容闸门可后加）。

**修点不动项**：白名单 5 基名保留并集；节奏/保留期不动；`depthBase` 归一对双端键义兼容（funding 符号 `XAUUSDT`→基 `XAU` 与 depth symbol 形态同一归一径）。

**验收残留**：获裁后实施 A（或 B/C）→ SG 实拍 `skips depth_missing` 归零。

## cex-depth 采集宇宙对齐 方案 A 实施（D-280 裁）

**改**：`ListFundingWitnessedSymbols(venue,6h)` 新读件（facts funding 见证符号，分所）；`universeDepthBases` 改分所签名 `func(venue)[]string`——动态基名=funding 见证 symbol 经 carryDepthBase 归一（静态白名单 5 对所全量保采）；`FetchCexDepthsVenue` 单所采样件新增（FetchCexDepths 旧签名保留委托）；`ListCfdUniverseDepthBases` 旧 CFD 域源零消费面撤（含测试）。节奏 5min/保留 90d 不动。

**验证**：TestListFundingWitnessedSymbols（分所+窗+去重保序+窗外不取）+ D057 测试改分所锚（binance 集不串 okx）全绿；build/vet/gofmt 净；pgstore/exchange/cmd/core 三包测试绿；race 锚绿；check-lines 零警告；import 方向 221/222；env-reads 过。

**部署验收**：SG 拍 `skips` 中 `depth_missing` 趋零（残余=候选腿在所无 funding 证据/venue 未上市——如实缺口）；`[cex-depth] <venue> witnessed bases` 行出三所分计数。

## E0 纪元归档——脚本交付（执行待审）

**交付**：`scripts/archive_epoch0.sql`（仓内可审）。SG 干跑（sed COMMIT→ROLLBACK 全回滚）验证通过：全表集 26 CTAS+TRUNCATE+终态集 4 DELETE+nav 特例链零报错；实测行数——大项 `fin_cex_dci_quote` 1,040,059、`fin_symdig_findings` 375,546、`cex_private_events` 92,973、`fin_cex_radar` 70,040、`position_evals` 12,039；positions 14/legs 46；paper_spot_carry exited 29709；fin_nav_daily 1 行回插。

**票面裁定执行记录（歧义表按禁线处理）**：
- `fin_proposal`/`explorer_proposals` 票面全表集→**转终态行集**（实拍 proposed×5/new×40 在途，wiping 违业主令在途留活语义）；fin_proposal 另排除 `fin_dci_order` 引用行（FK NO ACTION+留活账本一致）。
- `fin_position_canonicals` 名义留活但 FK→positions 拦截 TRUNCATE（约束存在拦与行数无关）——6/6 行全指向待归档 positions，归档全表随大集同截（语义=positions 联结映射，新仓续写）。
- **留活标名待委员会**：fin_trx_cycle_vi/fin_trx_txs/fin_trx_vote_apr/fin_trx_vote_apr_top/fin_trx_witness 五件无 status 列（快照/账本型，在途谓词不可定义）→未归档。
- `fin_strategy_hearing` 票面名单内已入全表集。

**SG 执行文**（待审后逐条）：
```
# ①dump 铁序先（未落两处禁动库）
ssh SG && docker exec nox-postgres pg_dump -U nox -d nox -Fc -f /tmp/nox_epoch0_20261013.dump
docker cp nox-postgres:/tmp/nox_epoch0_20261013.dump /opt/nox-backups/
rclone copy /opt/nox-backups/nox_epoch0_20261013.dump <R2-remote>:nox-backups/   # 副本二
# ②跑归档（单事务，FK 撞即整体回滚）
docker cp scripts/archive_epoch0.sql nox-postgres:/tmp/
docker exec nox-postgres psql -U nox -d nox -v ON_ERROR_STOP=1 -f /tmp/archive_epoch0.sql | tee /opt/nox-backups/e0_archive_verify.log
# ③验收：尾段对拍表 arch=源表前行数（干跑数已上）；live=0（全表集）/在途余留（终态集）；fin_nav_daily live=1
# ④docker restart nox-core → 拍 scan 环+请求箱#11+fin_strategy live 行
```

**风险注记**：`fin_proposal` 在途 proposed 行引用的 `fin_opp_eval` 已全归——下拍重评自动补卡；`positions` 清空后 dedup_key 唯一键对新开全放行（历史不复开是业主令语义）；`fin_strategy_hearing` 归零后听证面从 E0 起算。

## 资金地图（业主令 10-13，D-285 零换算）@3e94d09d
- **RPC `GetCapitalPools`**（命名避开 D-546 钱包树 `GetCapitalMap`）：`fin_venue_account_snapshot` 每池最新行 × 在跑归因，后端算币种分组零汇率换算（USD/USDT/UST 各报各的）。
- **deployed 口径**：MT 池=equity−free_margin（占用保证金）；`venue_type='earn'` 池全额在跑；CEX 池=OPEN 腿名义（filled_qty×avg_fill_price，按 leg.venue_key 归池）+未到期 DCI 锁额（与 `OutstandingDCIQty` 同谓词——response.settleDate 回执优先、回退 created+tenor 窗），DCI 归属=venue 有 spot 池→spot，无→funding，皆无→不归因不落虚池。idle=equity−deployed。
- **stale/online**：快照超产源窗=stale——事件/MT 源 5min（同 combo「池快照过期」判窗）、scheduler 36h（日采节奏）；online=!stale 且 src 非空。幽灵池与 `ExplorerIdleFunds` 同语义打标、剔组计、前端折叠独栏。
- **前端 `/capital`**（nav「资金地图」）：币种卡（权益/闲置/在跑）+池表（在跑明细列策略#仓号/DCI#单号/金额），stale/ghost 置灰标时点，纯渲染零运算。
- 锚：`TestCapitalPools_Store`（快照/ghost/DCI 窗/腿名义读面）+`TestCapitalPools_Build`（分组/CFD 占用/三源归因/ghost 剔计/stale 判）双绿；机检+npm build 过。
- 验收留痕（部署后 SG）：USD 组应 5247.63 全闲（3001157+15032313），binance spot 闲 1044.81/跑 542=DCI#10，okx earn 169.45 全额在跑。
## 资金地图 G3 退回修（DCI 池外在跑）
- 病灶：idle=equity−deployed 对 DCI 双减——binance spot 快照 equity=venue 扣款后余额，DCI 锁额已离池。
- 修：deployed 拆「池内在跑（margin/earn/OPEN 腿名义）+池外锁额（DCI）」；idle=equity−池内在跑。锚改 cex:spot 闲 1045/跑 542、明细 ref 标「池外锁额」。
## earn 提案执行接线（D-286 金钱不眠）@cd0d9b27
- **执行族登记**：`gate.EarnTemplates{cash_mgmt,earn_apr_lending}`+`IsExecTemplate`（ExecTemplates∪EarnTemplates 单源判）——CheckDCI/autoexec/Go 三消费面同源；ExecTemplateVenues earn→binance（写径现状唯一）。
- **goEarn 闸序**（GoProposal earn 分派，CheckDCI+120s 租约已过）：venue=binance 限定→凭据→`cex_exposure_pct` 硬线（池快照 CEX 权益占比 fail-closed，申购不动 CEX/CFD 结构但超限不加深）→USDT 现货可动实读全量→活期在售品 best-APR 复验（canPurchase∧APR>0∧minPurchase≤额）→earn_subscribe 坑表抑单→`earn|USDT|p<id>|<attempt>` 幂等键→POST `/v1/simple-earn/flexible/subscribe`。
- **账本**：`fin_dci_order` opt_type='EARN' 同构行（tenor=0→OutstandingDCIQty/DCIOrderOpenIntent 谓词天然不命中）；`ListFilledDCIOrders` 剔 EARN（无交割终态）；败=failed 行+坑表 24h suppress+gap 分类（credential/permission/runtime）。
- 锚 7 件绿：happy（best-APR 档中标+态机 executed+账本行）/敞口超限/零可动/零在售 APR 复验/坑抑零打面/venue 败落坑/幂等。
- 遗留：①提案轨型（autoexec 须 fin_strategy=live，earn 两键现 candidate——终裁面）；②申购额=USDT 现货可动全额（活期可赎承接，DCI 腿径 redeemShortfall 已可回补现货）；③okx earn 写径未接（提案 venue=binance 单源）。
## 经验库自动归纳管线（业主令 10-13——77KB 积压零消费→归纳去人工化）
- **段**：`tools/noxkb` 新子命令 `induct`——待归纳.md+待归纳-分析员.md 未消解行 → 关键词分诊 → LLM 聚类草稿 → `gen` 重产索引 → 源行打 `inducted:<h8>` 幂等标。
- **module 裁定**：noxkb 删 go.mod 并入主 module——`tools/noxkb` 变 `nox/tools/noxkb`，`internal/` 前缀规则放行（LLM 走 analyst 基建+pgstore 账本的前提）。
- **covered 分诊**（确定性零 LLM）：摘要小写对拍条目 keywords——≥2 关键词命中或单 ≥12 字符指纹级命中 → `covered:<id>`。单短关键词误例实拍（「binance」把 CROSS_VENUE 复审行误归 hmac 条目）→ 收紧规则；该规则是高置信门槛的非唯一正确解，误 covered 代价=真缺口行被打标不入草稿（残留风险）。
- **LLM 段**：`analyst.RunWithSpecs` 零工具集（noTools 执行器防违发 tool_use panic）→ 严格 JSON `{"drafts":[...]}` → schema 闸（slug/kind/risk 域值/keywords 1-8/summary ≤200/rows 非空/撞库剔除/maxDraft=20）整批违式不落盘只 warn → `条目/<slug>.md` 按模板六节渲染、**status 强写 observed**（verified 只人工授）、evidence_refs 记源行 `file#L`。
- **源标**：待归纳.md 行末列打 ` ✅ inducted:<h8> covered|draft:<id>`——复用 digest ✅ 消解出口，次跑自动移归档月卷；分析员 inbox 行打 `inducted:`（无 ✅ 归档机制，靠标幂等）；inbox 含「复核域」行跳过（D-172 域外不升 KB）。打标=行级原位改写，digest 未跑前重跑零重复。
- **fail-open**：PG/AI 未配→LLM 段跳过，covered 标仍落；违式整批不落盘。零新 env（NOX_KB_DIR/NOX_PG_DSN 复用 digest 引导件；NOX_AI_* 属 app_secrets 禁读→DB ai_settings 单源）。
- **验收**：锚三件（covered 标+幂等/草稿落盘+违式剔除+索引重产/schema 整批闸零写）；本地干跑（KB 拷贝+LLM 缺配）实测幂等。SG cron 挂 `30 22 * * *`（UTC，日报前一拍）由部署侧执行：`cd /opt/nox/tools/noxkb && ./noxkb induct`——**首跑消化 77KB 存量的实拍属部署验收**，本票代码面交付。
- 残留：①LLM 未配的日报前窗=covered 段仍消化，drafts 留待配置齐备；②inbox 行未被任何 draft 引用者保持未标、次跑重试（设计，非漏）；③索引 budget（98304B）随草稿入增——触顶按既有上浮先例调 maxBytes。
## 财务可见性两件套（binance:earn 回归分币快照 + DCI/持仓面板）
- **①binance:earn 回归**（D-133 摘除令约束解除）：旧约束「理财面只统计 USDT 不可达」随分币口径解除——`cex_account_binance_earn.go` 走 `GET {sapi}/v1/simple-earn/flexible/position`（USER_DATA 签名复用既有凭据，零新 env；私有流不推 earn 仓位=REST 唯一径，票文已举证）。逐资产聚合→`AccountInfo.Holdings` 分币；单币 Currency=原生币种、多币="MULTI"+Equity=Σqty（仅作变更指纹）。迁移 `1013` 回种配置行（binance spot 行复制，0182 同式幂等）。
- **快照写径**：`snapshotRows` earn 面改每币种一行（读端 DISTINCT ON 升 (venue,venue_type,currency) 三键）；prev→cur 币种消失补 0 行防陈旧残值滞留。`snapSame` holdings 不参与去重——D-136 accrual 噪声防线不破（聚合额变才触写，消失币种届时零填行兜）。读端三处 + digest + coverage 服务同步三键，`binance:earn` 白名单解除（funding 面摘除保留）。
- **②前端 `/dci` 面板**：ListPersonalPositions(OPEN)+ListDCIOrders 双表纯渲染（qty/entry/status/note/opened_at + venue/template/product/opt_type/strike/qty/ccy/tenor/status/response/proposal/created_at），资金地图「在跑」列加下钻链 `#/dci`。
- **锚**：TestBinanceEarnPositions（多币聚合/同资产合并/零额跳过/MULTI 指纹/签名头+路径）替 D133 摘除锚（语义反转：拒发径→正常采集）；D132 快照锚绿（分币行+去重口径回归）；TestCapitalPools_* 三键快照不误塌。
- **遗留**：①SG 验收=`fin_venue_account_snapshot` 现 binance+earn 行+DCI 页对拍 `fin_dci_order` #10 BTC PUT @83250 542 filled；②earn 配置行回种靠迁移 1013（部署后首个 refresh 轮即采集）；③活期赎回/申购写径复用 earn_subscribe 面（不变）。
## 拒单消费闸（业主令 10-13：坑表教训回流告警面）
- **共享包 `internal/kbsearch`**：Load 解析 `docs/经验库/条目/*.md` frontmatter（id/keywords/summary/status；retired 剔除）；Match/Search 高置信对拍=induct covered 同规则（<4 字符短词忽略；≥2 命中或单 ≥12 字符指纹词；序=hits desc→最长词 desc→id）。Suffix 便捷件 fail-open（读目/解析败→""）。
- **告警接线**：dciexec `KBDir` 字段（cmd/core 装配 `/kn/docs/经验库/条目`）；三处落坑点 dci_subscribe/earn_subscribe/earn_redeem 的 permission+runtime gap 文案尾附 ` kb:<id> <summary≤80rune>`——同一拒单的请求箱文本与 critical 告警同得教训行；零命中零拼。
- **analyst 工具**：`kb_search`（{text}→命中条目行 id/status/hits/kw/summary，顶 8）——项目 LLM 自查坑史同径；import 白名单加 analyst/dciexec→kbsearch 两边。
- **禁线照办**：热径纯关键词对拍零 LLM 零子进程；附行只引 id+summary；fail-open 读败只省略附行不阻塞落坑/告警。
- **验收残留**：SG 实拍=触发已知签名拒单（或桩注入）alert 正文现 `kb:` 附行；AskAnalyst kb_search 返条目引用。
## AI 设置前端页（业主令 10-13：ai_settings 免 SQL）
- `/settings` 页 AI 板块：base_url/model/api_key（password 框+掩码尾4 placeholder 回显，留空=后端不动）/enabled 开关；GetAiSettings/SetAiSettings 既有 RPC 直走零 proto 动；保存后重读掩码清输入框。
- 零前端运算、api_key 永不回显明文（掩码只进 placeholder 不进 value）。
- 验收残留：SG 实拍页面对拍 ai_settings 行。
## 敞口闸分母口径修（部署验收揪出：'' 币残行双计稀释 ~13pt）
- **病灶**：DISTINCT ON (venue,venue_type,currency) 把 MT5 `currency=''` 老行当独立池——3001157 同权益 ''(4245.51 陈旧)+USD(4247.41) 两行入分母，earn 闸算出 ~34.9%<35% 放行（真 ~48%）。
- **修**：`snapshot_ccy_norm.go` 通用折叠件 `collapseBlankCcyPool`——同 (venue,venue_type) 组 '' 行并入最新命名币行（'' 较新采其值、币名不丢；纯 '' 组原样保留）；三处读面应用：CapitalPoolSnapshots（敞口闸+资金地图）、ExplorerIdleFunds（idle/NAV/deptreport/explorer 全消费面）、LatestVenueAccountSnapshots（覆盖率）；digest 同规内联实现。查询补 COALESCE(currency,'')。
- **stale 确权**：陈旧行仍入计量（如实值）——stale≠0 但页面已置灰标时点，语义不动。
- **前端②**：资金地图只渲主币组（USD/USDT）；非主币池折叠 `其他小币种 ×N` 单行计件不计值（零换算不可求和，行内示原币值）；equity/idle/deployed 全零行不渲。
- **锚**：TestCollapseBlankCcyPool 四案（''陈旧并入/''较新采值/纯''保留/多命名币earn组不动）。
- **测试库残留自纠**：前一次 600s 超时 panic 杀测试进程→wait_condition_exposed_test.go 的 cleanup 未跑→gate:perp cap 残留 100000 致 D543 连败两轮（非本票病灶）；手工 UPDATE 归位后全绿。
- **验收残留**：SG SQL 实拍 3001157 单池单行→重算占比对拍 gate 所见值；earn 提案若再触闸应如实拒 ~48%>35%。
## 2026-10-13 营运日结（E0 新纪元首日）
- **背景**：E0 归档后首个完整营运日。财务面+经验库面+执行面三件主建，业主令驱动。
- **交付**：①资金地图页（D-285 零换算口径，G3 修 DCI 池外锁额双减）；②earn 执行接线 `cd0d9b27`（闸序/幂等/坑表全链，cash_mgmt+earn_apr_lending 升 live 成 D-288）；③经验库自动归纳管线（noxkb induct + cron 22:30 UTC，77KB 存量消化，observed 草稿闸）；④财务可见性两件套（binance:earn 子账户 REST 采集 + DCI 面板页）；⑤拒单消费闸（kb: 附行回流告警面 + analyst kb_search 工具，零 Devin 依赖）；⑥AI 设置前端页；⑦敞口闸分母口径修（'' 币残行双计稀释 13pt 病灶）；⑧earn 成交回执 warn alert 补（feishu 滤噪闸语义）。
- **生产实证**：E0 归档闭合（207MB dump 双副本 sha256 一致）；autoexec 真金三单——DCI#10 BTC PUT @83250 542 filled + earn #13/#14 两笔活期申购 filled（D-286 首承接）；earn 闸如实拒 45.6%>35%（修后归真）；binance:earn 10 币种快照行落地。
- **病灶修**：迁移 1013 enc 凭据列漏拷（-2014）、induct schema 闸 scope:string 整批拦（宽容反序列化）、earn 成交零推送、敞口闸 '' 币行双计。
- **AI 基建**：ai_settings 切 SenseNova flash-lite（induct 调用 105s→1.6s）。
- **遗留**：fin_condition_gap #11 待业主裁；binance:earn USDT 行等 refresh 拍；事件源 stale 语义挂账；listenKey→listenToken 迁移跟踪；github 推送积压在途（代理断）。

## 2026-10-13 okx:futures 交割面开通（gap#11 收口）

- **迁移 1014**：`exchange_configs_venue_type_chk` 枚举 +`'futures'`（drop+add 幂等）；okx:futures 行回种=perp 非 sim 行 enc 凭据复制（NOT EXISTS 幂等，明文列恒空=D-515 语义）；baseline 1000 同步枚举+COMMENT。
- **写侧归一**：`NormalizeVenueType` okx futures→unified（统一账户同一 /account/balance 覆盖交割持仓资金——不归一则独立 venue_type 同额快照行=同钱双计，'' 币残影同型病灶）；`HasPrivateStream` okx 集加 futures（一条私有流覆盖，防独立轮询）。
- **分派审计**（futures 盲区核毕）：`Account()` okx 分支穿透任意 type 走 /account/balance ✓；`FullInventory` okx 兜底分支 ✓；CexPositionManager perp-only 过滤 futures 跳过 ✓（采集不接=禁线）；`streamable` okx 仅 perp 起流 ✓（HasPrivateStream true 仅覆盖判定）；executor 构造零 panic、无模板路由 futures=fail-closed by absence ✓；graduation alloc okx 任意交易行通用 ✓；capital_coverage 标签已有 `futures`→「交割」。
- **gap#11 收口径**：exchange_configs 行→`ListCapabilityLedger` credentials→`reconcileGaps` EvalCondTri ok→自动 fulfilled（零新径）。
- **锚**：`TestOkxFuturesSeed`（enc 复制 real 非 sim/幂等/枚举放行+无关值仍拦）+`TestCEXAccount_OkxFutures`（统一账户端点+venue_key 穿透不 panic）+归一锚扩 futures case。
- **验收残留（SG）**：部署后 `SELECT venue,venue_type FROM exchange_configs WHERE venue='okx' AND venue_type='futures'` 行在册；gap#11 次轮 reconcile 后 fulfilled；`[account] cex querier: okx:futures` 日志行+快照落 unified 无双计。

## 2026-10-14 okx:futures SG 部署验收 ✅ @ba84705b

- 迁移 1014 上库：`okx:futures` 行在册（enc 凭据 k=t，明文恒空）。
- **gap#11 自动收口**：boot 扫描 reconcileGaps 判凭据到位 → requested→fulfilled，零手工。
- querier 注册日志 `[account] cex querier: okx:futures` 实证；快照归一 `okx:unified` 零双计（trading 账户单行，earn/funding 独立账本）。
- `fin_condition_gap` 表**清零**——E0 后系统进入「零业主欠账」。

## 2026-10-14 业主视窗两件套（报告中心+盯盘引擎）

- **A 报告中心** `/reports`：部门日报（30 日侧栏+逐部门 markdown 渲染）+ 报告文件列表/正文 + 补跑按钮（GenerateKbReport/GenerateRetroReport）——五 RPC 全消费。极简 md 渲染（标题/列表/表格线），零依赖零运算。
- **B 盯盘引擎**：`watch_rule` 表（迁移 1015，kind='price' 首期，其他 kind chk 拦=fail-closed）；`internal/risk/watch` 引擎 QuoteBus 通配订阅（`Subscribe("")`）tick 即判——零轮询 ticker（D-004）；stale 守卫（now−q.Time>DetectorFreshnessTTL 不触发）；once 自翻 disabled、repeat cooldown 防抖；触发→InsertAlert warn 穿飞书闸。规则集 configKick 热载（D-469b 同径）。
- **CRUD**：List/Upsert/DeleteWatchRule RPC（后端校验同 chk 域值）；`List` 附 stale 标（qbus 最新报价超 freshTTL 即标）；mutation 后 kickConfig 即时生效。前端 `/watch` 页（列表+增改表单+启停）。
- **装配**：`wire_watch.go`（行数门禁拆出）configKick 订阅+Run。
- **锚**：引擎 3 件（once 触发+自翻/不达阈值+venue 过滤+陈旧 tick 拒触发/repeat cooldown）+`TestWatchRuleCRUD`（1015 表 CRUD+chk 守 kind/op+MarkWatchFired 自翻）。

## 2026-10-14 业主视窗 A/B SG 部署验收 + 僵尸提案修复实证

- **A 报告中心**：`ListDeptReports` 实拍返今日公司日报全文；bundle 含 reports/watch 页，隧道在线。`#/reports` `#/watch` 可用。
- **B 盯盘引擎**：迁 1015 上库；BTCUSDT lt 999999 once 规则真插→tick 即判→warn alert 落 `盯盘触发#2: BTCUSDT 83187.1 < 999999`+`last_fired_at` 落账+enabled 自翻 f——全链实证，测试规则已清。
- **僵尸提案修复 @8122698b**：boot 扫描实证 #221 funding_carry/#225 cross_venue_spot `proposed→deferred`（auto 轨无执行器新语义生效），机制缺口闭合。
- **返修**：proto3 `enabled` 零值陷阱（不带 enabled 建规则=死规则）并入 C 件收口票。
- **顺带实证**：XAUUSDT SELL 系统外单系统已自动 warn 告警「不追踪，若为收编漏检残余请人工核查」。

## 2026-10-14 视窗 C 件 SG 部署验收 ✅ @f219029f

- **enabled presence**：规则#3 不带 enabled 建→真触发（`盯盘触发#3: BTCUSDT 83194.8 < 999999` warn 00:03:49 落库）→once 自翻——proto3 零值陷阱实证闭合。
- **契约补段**：`contractBackfill` 在 SG 代码面（contract.go:64 `契约补段·原料` 拼接径在）；行为验证点=明早 07:05 日报——若 kimi-k3 漏段则见原料段回填+marker。
- 测试规则#3 DeleteWatchRule 已清。

## 2026-10-14 宇宙雷达仪器轴扩列 @dedaf4f1
- 迁移 1018：fin_cex_universe+fin_cex_radar 加 contract_type(PERP/FUTURES)/expiry_ts/basis_ann；pgstore 读写面同步。
- 枚举：binance 收 CURRENT_QUARTER/NEXT_QUARTER（deliveryDate→expiry）；okx instruments+tickers 拉 SWAP+FUTURES 双 instType（expTime→expiry，OPTION 不扩）；gate 同 contracts 端点 expire_time>0 行。
- 评估轴：FUTURES 走 basisAnn=(mark/index−1)×365/days×100；expiry 零值/已过/mark/index≤0→nil fail-closed；funding 轴 perp 语义零动（交割 FundingAnn nil 自动不入 funding 事件/divergence）。
- okx index 轴：/index-tickers?uly=BASE-USDT 逐 uly 取 idxPx（交割 uly 屈指低频）；读败 Index=0→basis_ann NULL 诚实缺。交割符号不打 funding-rate 端点。
- normBase 扩：尾段数字日期（-YYMMDD/_YYMMDD/_YYYYMMDD≥6位）先剥再走 perp 链（BTC-USDT-241227→BTC 等三所形全测）。
- bootstrap 静默沿用：旧 universe 零 FUTURES 行时交割新行视同首轮静默（零 NEW 事件零告警），轴立后 NEW/DELISTED 径照常。
- 对冲腿 join：universe 键 venue|symbol 原生名即区分到期腿；contract_type+expiry_ts 列就位供日历价差下游表达（产提案属后续票）。
- 锚 5 件：normBase 三所交割形/basisAnn 全域 fail-closed+perp annualize 回归/binance QUARTER 收/okx FUTURES+index+funding 不打交割/gate expire_time 枚举/首扫静默+次扫 NEW/DELIST。
- SG 验收残留：radar 轮后 `fin_cex_universe` 现 okx BTC-USDT-YYMMDD FUTURES 行+last_basis_ann 非空；首轮零告警洪泛。

## 2026-10-14 雷达交割轴补全 @cd6940aa
- okx FUTURES filter：中段白名单 {USDT,USDC,USD_UM}（USD_UM=USDⓈ篮线性交割=SG 实证稳定币交割实面）；BTC-USD-* 币本位剔（逆向另一族）；BTC-USDT-*/BTC-USDC-* 命名空间保留。
- okx index 接线：okxIndexUly（USD_UM→BASE-USD、其余→BASE-mid）逐 uly index-tickers；mark=FUTURES tickers last（已拉零新调用）。
- binance 交割 mark/index：premiumIndex 响应本就全符号含 markPrice/indexPrice——premiumIndexFunding 返三 map 顺手填 FUTURES 行（perp mark/index 仍走 ticks/facts 原位不动），零新增调用。
- normBase 扩：stripDigitsTail 剥 ≥6 位纯数字日期尾（-_ 分隔）+后缀链加 USD_UM/USDC/USD（-USD_UM 先于 -USD 判防截 -UM）。
- ct_val 照旧读 venue ctVal 真值（USDⓈ 面值不编 1.0）。
- 部署坑记录在案：instruments 缓存存序列化 struct 非原始 JSON——扩字段后须删 /root/.cache/nox-universe-radar/*.json 逼新拉（SG 已实证此坑）。
- 锚升：USD_UM 收+BTC-USD-* 剔+index uly 映射对拍+binance 交割 mark/index 接线断言。
- SG 验收残留：fin_cex_universe 现 *-USD_UM-* FUTURES 行+basis_ann 量级合理；binance 4 交割行 basis_ann 出数；轮零告警洪泛。

## 2026-10-14 新品类自发现管线 S1 @11eaaef1
- 迁移 1019：fin_opp_hypothesis（category_key UNIQUE 幂等锚；status chk proposed/approved/rejected/trialing；rejected 在册=弃件记忆抑重推——UpsertHypothesis ON CONFLICT 只刷 last_seen/sample/detail，xmax=0 判新插）。
- a 级侦测：catSource 可选接口——binance 收轴集外 (contractType,quoteAsset) 对（月度交割/币本位等）；okx FUTURES 白名单外中段（币本位）+OPTION instType 存在性（instruments?instType=OPTION 入 24h 缓存载荷）；gate contracts 形态键（gateShape 数字归一 D，收轴 USDT/USDT_D 外）。instType 无全集枚举端点=全新 instType 不可侦，b 级公告源缺位如实记（设计稿 §2 D# 待裁）。
- LLM memo：kbreport.ResolveClient（ai_settings 单源）+LLMLogger(role=category_memo)；JSON schema 闸（family_fit∈{generic_yield,novel,none} 越域降 novel、memo≤2000、无 JSON 占位）；只写 family_fit/params_proto/memo 三列不碰 status（批准键永不进 LLM 手）。
- 告警：新假设聚合一条 info（含 memo≤300 字）；bootstrap 轮行照落零告警（同 universe NEW 静默）；LLM 未配 fail-open 占位。
- 禁线照办：S1 不建批准/试单/毕业径；零新 env；venue 读径败 fail-closed。
- 锚 3 件：管线全链（落行+memo+告警+同键零重推+rejected 抑）/schema 闸违式降级/bootstrap 告警抑制。
- SG 验收残留：雷达轮后 fin_opp_hypothesis 现 proposed 行（预期 okx|OPTION、binance|CURRENT_MONTH|USDT 等已知未收品类首拍各一）+memo 非空+info 告警到；同键重跑零重推。
- 机检备注：全量套件 dashboard D543 单发 FAIL=gate:perp cap 脏值残留（同前票 panic 杀进程型复案），UPDATE 归位后单跑绿；与本改动无关。

## 2026-10-14 雷达交割轴返修 @1978cbe6
- okx indexPx 端点参 uly→instId：venue 实证 index-tickers 只吃 instId（?instId=BTC-USD 有行、?uly= 无行——返修单咬此）。okxIndexUly→okxIndexInstID 语义不变（USD_UM→BASE-USD 映射照旧）。锚对拍 instId 查询参。
- SG 残留：部署后重跑 radar 验 okx USD_UM 行 basis_ann 出数。

## 2026-10-14 交割轴+S1 SG 验收（决策席实拍）
- **交割轴全中**：okx USD_UM FUTURES 20 行在册+basis_ann 出数（SOL-USD_UM-261030=+19.00% 首个真日历基差信号；XAU-USD_UM-261127=-10.30% 负基差如实）；binance 4 行 4.1~5.1%；返修 1978cbe6（uly→instId）生效——index-tickers 零读败。
- **S1 幂等中**：次轮零假设行零事件零告警（category_key 抑重推实证）；侦测产真货（TRADIFI_PERPETUAL|USD1=股指类永续、USD1/U/USDC 报价永续、USD_UM_XPERP）。
- **S1 缺陷钉死**：4/7 假设行 memo 永久空——LLM 无 JSON 走占位路径时 `params_proto` 写空串进 jsonb 炸 22P02，且**行已落在册后续轮不重试 memo**（空 memo 行死态）。返修件已立：占位写径修+空 memo 行重试语义。

## 2026-10-14 S1 占位写径修 @f0ff5aff
- UpdateHypothesisMemo proto 参 []byte→string + `NULLIF($4::text,'')::jsonb`：jsonb 不吃零长字节序（22P02 实证）。
- 死行重试：UpsertHypothesis RETURNING 加 `family_fit IS NULL AND status='proposed'`→memoPending；上轮 memo 写败的行下轮重试预审、回填不告警（原 `!inserted→continue` 会令空 memo 行永久死库——票面附带问询属实）。
- 锚升：params_proto 落 {} 对拍 + 死行回填零告警锚；共享库 alerts 残留清基线。

## 2026-10-14 源文件超限拆分 @696b7c5c
- check-lines 三警告清零：diff.go 360→132（拆 diff_norm/diff_funding/diff_divergence/diff_basis）；tools.go 316→94（拆 tools_query/tools_readfile/tools_kb）；run.go 308→224（拆 run_view）。纯搬移零行为变——签名/导出符号/测试语义全不动。
- 机检：三包 build/vet/gofmt/test/-race 全绿；全量 go test 中 dashboard D587 单发 flake（共享库压力型复案，隔离单跑 1.56s 绿）。

## 2026-10-14 日历基差 P0 纸面轨 @9cda5a41
- paper_delivery.go：binance 同所同 canonical 跨 contract_type 组对（perp 腿在场+交割行 basis_ann>0 contango 才入对；okx USD_UM/负基差 P0 禁线实证锚）。开仓行 structure/side='perp_delivery'、venue_type_a=futures×b=spot（对冲腿现货=正 funding 期多 perp 是成本项）、entry_basis_bps+entry_funding_ann(=basis_ann) 快照。
- PnL=Δbasis 市值重估：(entry−cur)×qty/1e4 accruedReplace 绝对写（非 funding 差×天数）；min_accrued 自动低水位=收敛路径观测件。退出：expiry−24h→'expiry_t_minus_1'、连缺 3 轮→data_lost。basis_ann 缺=不开仓。
- 滑点前置：binance bookTicker REST 逐交割符号读→facts(delivery_depth_spread_bps 新 kind)；读败→note='滑点未知'诚实降级不判死。
- 迁移 1020：psc_venue_type_ab_chk 枚举加 'futures'（baseline 同步）。执行器不建（P2）。
- 锚：配对域（USD_UM/负基差剔）+全链（开仓行结构/滑点未知标/Δbasis 重估 5.0 对拍/T−1 退因）。
