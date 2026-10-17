# decisions — 决策记录

> 一行一决策：编号 · 日期 · 结论 · 理由（一句）。顺序取号；引用旧编号写 `旧D-xxx`。
> D-001~D-211 出处=归档仓 `/opt/arb-archive/docs/handoff/decisions.md`（只读）；D-212 起续号。

| D# | 日期 | 结论 | 理由 |
|---|---|---|---|
| D-212 | 2026-10-12 | **归档重建总裁定（业主全权授权决策席）**：旧仓 `/opt/arb`→`/opt/arb-archive` 只读归档（hotfix-only 至切流），新项目仍名 `arb`（module/目录不变）按 `docs/公司架构.md` 十一部目录重建；**搬迁不重写**（包带测试整搬、filter-repo 保史）、入场有门（部门目录+dNNN 改名+D# 注释清+模块页+import 红线）、数据一行不动（`0001_baseline.sql`=生产 schema dump）、纪律原样继承（AGENTS §2–§8 全文、hooks/allowlist/skills 逐字节、D# 续号）；**不搬**=CMC 面/price_alerts/investment_records/INTRA 沙箱/capital 4 空表（归档可取，反裁走 D#）；TRX 托管资产线搬（D-066 账本隔离不变）。票序 M0 归档→M1 骨架→M2.1–2.6 按部门搬→M3 对等闸+切流→M4+ 六新件；裁决面=`docs/搬迁清单.md`。 | 收益=甩非基金业务约三成代码+文档债归零；代价=双轨期 hotfix 移植；XAU carry 休眠、仅 DCI PUT 线在跑=盈利验证低谷期，机会成本最低。 |
| D-212R1 | 2026-10-12 | **GitHub 复用不改名**（业主令「可清空」）：`myfxlogs/arb` 继续作新仓远端；旧历史保全=M0 先推 `archive/2026-10-12` 分支+tag 并 `ls-remote` 核在，M1 首推新 master 一次性 `--force`（唯一例外，此后 ff-only 不变）；本机 `/opt/arb-archive` 为第二副本，备份三落点 ③ 不缺位。业主同时明示：**两席位工作方式保留**（`[角色:…]` 开工指令、`[施工完成:<票号>] @<hash>` 尾行、实施≠复审）——随 AGENTS §3/§4 原文搬入。业主条件请求清零。 | 原裁定要业主改名+建仓两动作；业主答「可清空」=授权覆盖远端 master，但代码远端副本是备份落点之一不得真清——以同仓归档分支替代改名，零业主动作且不丢落点。 |
| D-213 | 2026-10-12 | **前端重写（业主令「前端可以重写，现前端非最优解」）**：`web/src` 旧源码**不搬**（D-212「搬迁不重写」的唯一业主明示例外）；M2.5 收窄为「API 面 + IR 后端」（dashboard RPC service 拆搬、auth、notify、inboxfmt、proto）；新前端=票 **M2.5b「运营看板 v1 + 投资人页 v0」**，M3 前落地（`//go:embed web/dist` 需真实产物）。**RPC 去留规则**：被飞书/digest/tools/scripts/运维操作（app_config 写、KillSwitch、探针审批、平仓、提案 ack）消费的 RPC 全留；仅被已废 tab/卡消费的纯展示 RPC 删（实施席 grep 举证逐条列入票面，复审核）；`StreamUpdates` 留。**前端硬约束不变**：零信任（运算/校验/分页/排序全后端）、只订阅 `StreamUpdates`、禁 `setInterval`/本地缓存旧值当现值、文件 ≤300 行、TS 客户端由 `buf generate` 产出、`web/dist` 不入 git。**技术栈**：React 19 + 纯 CSS 维持（AGENTS §8），换栈须单独 D#；**设计先行**：M2.5b 施工前先交一页 `docs/modules/web.md`（页面清单 × 每页消费的 RPC × 数据流 topic），决策席批后再写代码。 | 旧前端 94 组件/13 tab 多为逐票长出的监控卡，与十一部职责不对齐；重写比拆搬 169 文件 dashboard 更便宜且顺带收缩 RPC 面；业主用飞书为主接口（D-033），前端为决策席/运维看板+投资人页，范围天然小。 |
| D-214 | 2026-10-12 | **新仓迁移号段自 1000 起**：M1 的 `0001_baseline.sql` 在 M2.2（迁移器包随 store 搬入时）改名 `1000_baseline.sql`，后续 `1001_d212_drop_unmigrated.sql`…；M3 切流 step 3 = `TRUNCATE schema_migrations` 后写入 1000。`cmd/migrate` 临时验证器随 M2.2 删除（无死代码）。 | 生产 `schema_migrations` 存旧号 1..201；新仓若沿 0001/0002 起号，迁移器会把 baseline/DROP 误判为「已应用」而静默跳过——号段隔离让碰撞不可能，比靠切流手工清表更稳。 |
| D-215 | 2026-10-12 | **M2.1 复审裁定（决策席）**：① **编译闭包原则**——票文包单外被本票范围 import 的包随票提前搬（本次实证：dephealth/inboxfmt/notify/scan[剔 intra]/verification/gen+proto dashboardv1），后续 M2.x 同例、实施席票面举证 import 边；② **pgstore 整搬**（235 文件，按表族强拆=重写）——后续票不再按表族分文件；不搬表族文件（`cmc.go`/`cmc_query.go` 及 notify/retention/table_health 内引用、gen cmc 类型）暂留，M3 表 DROP 同票清除（记 M3 票注）；③ **跨票依赖测试挂起制**——依赖未搬包的测试文件挂起记票注（`mt_swap_d342r1`→M2.2、`d475b_contract`/`d566b_okx`→M2.3），随依赖到位回补并计入对拍差额；④ **D# 注释政策**=`// D-NNN` 行首锚式清除改行为描述、行内出处引证保留；⑤ 旧迁移 0001–0201 与 baseline 并存=测试重放源（pgstore 测试经 `arb/migrations` 重放全史），M2.2 迁号段时并裁文件去留。 | 票文按字面不可编译——数据部包反向 import 研究/IR/复盘/运维包、M2.1 工具 import notify；闭包提前搬=唯一零语义改动解，优于断依赖或重排票序。 |
| D-216 | 2026-10-12 | **M2.2 范围修正（决策席）**：`settleadapter`（单文件，import autotrade）与 `reconcile`（10/59 文件 import exec）改判 M2.3——两包对未搬包是**源码级**依赖非测试级，D-215③ 挂起制不适用；反向把 exec（142 文件）拉进 M2.2=掏空 M2.3 核心，得不偿失。M2.2 实办=settlement/swapledger/review/mmm/sysretro/kbreport（verification 已在仓 D-215①）+ **D-214** 号段（`0001_baseline.sql`→`1000_baseline.sql`、删 `cmd/migrate`、旧 0001–0201 文件去留并裁）+ `cmd/resettle`/`settleonce`→`cmd/ledgerctl` + 回补 `mt_swap_d342r1`。另记 M2.3 待裁：autotrade 依赖 capitalview/econ/spread(M2.4)+dashboard(M2.5) 四包，届时按 D-215① 举证或回问设计者。 | reconcile 的 exec 边=auto_close 执行面（清单 §1「auto_close 子域随 trading 调用」原文即指此），随 M2.3 与调用方同侧搬，比反向拉 exec 进账本部票更贴部门边界；settleadapter 本是 autotrade 装配件，同票最自然。 |
| D-217 | 2026-10-12 | **M2.3 依赖收口（决策席，接清单 §11「Q9–Q11 就地裁」授权）**：① dciexec 自带闸=纯文件移动至 `internal/risk/gate/dci_*.go`、trading 侧仅改 import（清单 §11 Q4 已统一 §1/§9）；统一 `risk.Check` 单入口=M4 新件不提前。② autotrade 未搬依赖实查：**非测试源码对 dashboard 零 import**（`engineService`/`marginFloorChecker` 窄接口注入，D-508 S7b 已解绑）；对 capitalview/econ/spread(M2.4) 有源码级依赖→按 D-215① 随票提前搬（capitalview 另依赖 reconcile=本票内；spread 按 §1 并入 econ）。③ autotrade 10 个 import dashboard 的**测试文件**按 D-215③ 挂起→M2.5 回补（d311_xasset/d328_carry_regime/d330_xasset_allow/d335_entry_gate/d406_rotate/d473_funding_hedge/engine_exit_events/engine_kick/engine_swaparb_margin_budget/funding_need）。④ M2.6 计数更正：wire_*=30 装配搬+7 测试随搬（cmc/intra/pricealert 三装配不搬，清单 §11 Q7）；tools 余 4=arbkb/credread/digest/ro-rotate（universe-radar/xasset-monitor/cex-proxy 已随 M2.1）。 | autotrade→dashboard 边实为测试级非源码级——窄接口注入让拆搬可在 dashboard 落地前完成；capitalview/econ/spread 叶级小包拉早零级联。 |
| D-218 | 2026-10-12 | **M2.2 复审裁定（决策席）**：① `cmd/resettle`/`settleonce`→`cmd/ledgerctl` **改判 M2.3**——实施席引 D-216 先例且实查属实（resettle→autotrade+reconcile、settleonce→autotrade+settleadapter，全为 M2.3 源码级依赖），追认；② 旧迁移处置=**留、迁至 `migrations/replay/`**：181 文件逐名对拍归档 identical，embed 路径改写，pgstore 测试全史重放绿；③ baseline marker 语义验收：`migrate.go` ≥1000 行在→<1000 不判 pending（squash 取代）、baseline 只补 marker 永不自动执行 SQL、鲜装走 1..201 链——与 D-214 切流序（TRUNCATE+写 1000）自洽；④ `migrate_baseline_test.go` 为本票**唯一新增测试文件**（对拍 114 vs 归档 115 的 +1 差源），属 D-214 结构面的覆盖件、非语义改动，追认；⑤ cmd/migrate 已删（-44 行）；⑥ `migrate_registry_test.go` 随 migrate.go 结构性改写（表 0001→1000）。 | ledgerctl 改判与 D-216 同型——源码级依赖不能挂起，拉 autotrade 进 M2.2 又掏空 M2.3；replay/ 子目录隔离让「重放料」与「生产号段」物理分面，优于原混放。 |
| D-219 | 2026-10-12 | **M2.3 闭包补件（决策席，放前实证）**：`detector`+`evaluator` 提前搬入 M2.3——`engine.go`/`engine_setters.go`/`engine_cex_paper.go` 三源码文件硬依赖（detector 叶级零未搬依赖、evaluator 仅依赖 detector、同批拉入零级联），同 D-215①/D-216 源码级先例。连带效果：engine 5 个 import detector/evaluator 的测试文件（d051_prealow/engine_publish/engine/notify/xasset_integration）**免挂起**随包搬。M2.3 挂起面收敛=autotrade→dashboard 10 测试（D-217③）唯一项；`sleepmon`=归档名即 M2.4「idle」（→`internal/portfolio/idle`），「gate」非包=autotrade `gate*` 文件拆入 `internal/risk/gate`。 | 放前把 M2.3 全包未搬依赖扫完一轮：唯二新增边即此组；放行时不留「待裁」。 |
| D-220 | 2026-10-12 | **前端组织架构页（业主令）**：M2.5b 必含「公司架构页」=十一部树×员工名册×**在线状态**（running/beat_ts×cadence/exits/当前 task），数据源=`fin_employee_registry`（D-190 心跳即 loop 节拍）+ `ListEmployees` RPC（既有，M2.5 随面）；**Dept 枚举十一部对齐由 M2.6 提前至 M2.5**（仅 `pgstore/employee.go` 枚举+seed 迁移，包本在仓，与 wire 装配解耦）——否则架构页先显示旧八类。部门级健康聚合口径（部门内员工 beat 超 cadence×N=异常）与页面分组排序属零信任后端职责，web.md 设计页须给出对应 RPC/字段。 | 考勤数据 D-190 已完备，纯消费面；枚举前移是因页面前置依赖而非装配依赖，迁移+枚举改动与 cmd/core 解耦。 |
| D-221 | 2026-10-12 | **M2.3 实施记录（实施席，随票报备；决策席全数追认见 D-222）**：① import 红线精修——`risk/gate→trading/exec`、`risk/gate→portfolio/capitalview`、`risk/selfwatch→research/scan`、`ledger/reconcile|settleadapter→trading/exec`、`ledger/settleadapter→portfolio/lifecycle` 为 D-215/216/217 钦定拆分的固有边（原同包内部引用），红线改为禁「risk/ledger→trading 引擎侧（autotrade|engine|dciexec）」反向依赖，白名单按搬迁后 170 边实冻结；② 挂起面=D-217③ 钦定 10 件 + 连带 3 件（d563_live_gate 引 d330/d335 夹具、d311/d423 内 dashboard 依赖段切出）——夹具本体在 dashboard 文件中无法提前复活，M2.5 零改回补；③ `oppmatch` 按清单 §9 归 M2.4（新树零源码引用，其测试对拍差额落 M2.4）；④ `cmd/resettle`+`settleonce`→`cmd/ledgerctl` 子命令合并（D-218① 落地，`ledgerctl resettle|settleonce`）；⑤ 共享件归属——`Config`/`SwapArbComboSnapshot`/`ControlPlane` 归 gate 包（Engine/Gate 双方引用，跨包禁方法故以类型所有者定包），`FamilyResolverAdapter`/`EliminatorAdapter`/risk_family 归 lifecycle，engineService 窄接口（D-508）保持 autotrade 零 dashboard 引用；⑥ gateSpecCache 序依赖修复——t1_events_backfill 夹具 spec（order_size_min=1）入包级缓存污染下游 d564 T5（改名后文件序变化暴露），补 `t.Cleanup(GateSpecInvalidate)`，归档仓同名序依赖原即存在未发作。 | 红线原按「部门整包禁」写死于空壳期；搬迁落地后同包内部边自然转跨包边，按「引擎侧反向」精修保留原意图（risk/ledger 不得反向依赖执行引擎）。 |
| D-222 | 2026-10-12 | **M2.3 复审裁定（决策席）**：① 机检独立重跑全绿——build/vet=0、gofmt 空、import 方向 170/170、`go test ./...`=2544 过/78 包、race（trading+risk+portfolio+reconcile+research）=1359 过/19 包；② **测试函数级对拍**——归档 autotrade 462 测试函数→新仓 404，挂起 **58 函数全部有名有姓**（D311×1/D328×3/D330×4/D335×1/D406×7/D423×2/D473×6/D563×5+engine 系 29）、零新增零静默，D-221② 函数级切出追认（粒度优于 D-217③ 整文件挂起）；**M2.5 回补清单=此 58 函数**，复核命令=归档 `autotrade/*_test.go` 与新仓 `trading/autotrade+risk/gate+portfolio/lifecycle` `^func Test` 名集合差；③ 拆搬结构验收——dciexec/leg.go 闸段→`risk/gate/dci_leg.go` 完整拆分（executor 面留 leg.go），Config/SwapArbComboSnapshot/ControlPlane→gate、FamilyResolverAdapter/EliminatorAdapter/risk_family→lifecycle、autotrade 零 dashboard 引用复核属实；+5 文件=带出处头拆分件（含 teststore_test.go 夹具同型复刻，认）；④ **红线精修追认**——risk/ledger→trading 引擎侧（autotrade|engine|dciexec）反向禁保留，白名单 170 边冻结；⑤ gateSpecCache 序依赖修复追认（单行 `t.Cleanup(GateSpecInvalidate)` 测试侧，归档原存未发作）；⑥ **D-220 撞号解**——业主令面留 D-220，实施席报备行改号 D-221（其分支基线先于 D-220）；⑦ 流程注记——实施席在部署树 `/opt/arb` 直接 checkout 票分支施工（契约=`/opt/arb-wt/` worktree），记录在案下票改正；本票 STATE 用「报审」态未自翻 ✅，较前两票规范。 | 拆搬=本工程唯一授权结构改动，逐块复核：全部归属判定有出处头+引边佐证，无自由发挥；序依赖修复是改名副作用的必要收尾而非语义改动。 |
| D-223 | 2026-10-12 | **M2.4 复审裁定（决策席）**：① 机检独立重跑全绿——build/vet=0、gofmt 空、import 方向 180/180、`go test ./...`=2643 过/82 包、race（research+portfolio）=364 过/12 包；四包文件/测试数与归档逐一相等、语义抽查零差异；② `dci_templates.go`（ExecTemplates/ExecTemplateVenues）自 dciexec 移入 `risk/gate` **追认**——oppmatch(research) 读径触发 research↛trading 红线，模板组=闸域件、DCILeg 已先在 gate；dciexec 改消费 `gate.ExecTemplates` 单源零重复；③ ExecTemplates venue 衍生硬编 `"binance"`=归档原样逐字（20 处 `case "binance"` 两侧同数），executor 单所写径事实，venue 化归 M4；④ go.mod 增项=trxvote TRON 栈（btcsuite/btcec v2.3.6 + gotron-sdk v0.26.0 + x/sync 升 direct），版本与归档逐一相等、零漂移，longbridge/pdf 等未搬消费方依赖随 M2.5 tidy；⑤ `docs/handoff/m25_suspended/`（M2.3 带入件）机制验收——挂起测试整文件存 `.go.txt` 防误编+README 清单；**实测 parked 68 funcs ⊋ 挂起 58——多 10 个为在仓改名件重叠（d311 整文件停存），M2.5 回补按函数级缺口取件勿整文件覆盖**；fixture_helpers 含 d335SeedRegime/d335Engine/d330 系夹具（依赖 dashboard.Service）随回补同归。 | 跨部门读径抽出属拆搬必要结构件，与 M2.3 同型；挂起件存仓可复审可回补，优于只留归档副本。 |
| D-224 | 2026-10-12 | **M2.4 实施记录（实施席，随票报备；决策席全数追认见 D-223；原误编 D-221 撞号改此）**：① `ExecTemplates`/`ExecTemplateVenues` 自 dciexec 移入 `risk/gate/dci_templates.go`——oppmatch 读径撞 research↛trading 红线，模板→腿组成属闸域件（gate 已持 DCILeg/CallLeg/PutLeg），dciexec 消费改 `gate.ExecTemplates` 读径零语义；② `sleepmon`→`internal/portfolio/idle` 包名 `idle`（清单钦定路径），log tag `[sleepmon]`/注释指称保留=行为串不改写；③ go.mod 增 `gotron-sdk v0.26.0`+`btcd/btcec/v2 v2.3.6`（trxvote 外链依赖，与归档同版）；④ 能力账本投影=pgstore/capability.go 已在仓（票行预审），消费面随 explorer/oppmatch 落地零补件；⑤ 四包对未搬包（dashboard/auth）零依赖实证——票行预审属实零挂起零提前搬。 | ExecTemplates 含 venue 衍生值（binance 单所写径），随腿映射随迁属同口径事实件；若 M4 拆 venue 维度再裁。 |
| D-225 | 2026-10-12 | **M2.5 放前裁定包（决策席，实证落票面）**：① dashboard **整搬** `internal/api/dashboard`（169 源文件共享 `*Service` receiver，强拆=重写——D-215② pgstore 整搬同型；§1「监控组件按 §4 归属」引文失效——§4 为前端段，组件重分发归 M4 重构域）；auth→`internal/api/auth`（2 源文件零 internal 依赖）；② **族件剔除**：`service_cmc_watch*`/`service_cmc_equity_alert*`/`service_price_alert*`（cmc/pricealert 不搬族，§3）+ `service_hedge.go`+test——GetHedgeStructure/ListHedgeFlow 数据源主体=fin_cmc_*（baseline=fin_cmc_positions.entry_price、liq=closeout_price、PnlVsBase=cmc 真值、cmc.AlertsToState），对冲腿即 CMC 域随死，proto 删此二 RPC 后文件成孤儿；若 ListHedgeFlow 实有非 cmc 主源数据举证则可留（剔 cmc 分支），实施席按响应字段逐条举证；`service.go`/`service_setters.go` 内 cmc 字段/设值器（cmcWatchTopic/cmcWsHealth/cmcLiveProvider/cmcFrame*/cmcEquityAlert/SetCmc*）随 proto 消息删除连带剔除——死接线非语义改动；③ **proto 编辑面**（授权语义面）：service.proto 删 PriceAlertRule CRUD×4+hedge×2、stream.proto 删 CMCWatchUpdate、orphan 消息与 cmc_* 字段以「仅此族消费」逐条举证删；`buf generate` 重产 gen（diff 面=删除件）；④ **RPC 审计面**：115 rpc 逐条列消费者（飞书/digest/tools/scripts/运维=留；同 consumer 组可分组举证），剔除项=②族件+废 tab 纯展示件逐条单举；⑤ **Dept 枚举落地**（D-220 细化）：新件 `pgstore/employee_dept.go` 十一标签常量 `数据/研究/投资/交易/风控/运营/账本/复盘/IR/运维/合规`（§2 roster 行钦定名）+ 迁移 `1001_*.sql` UPDATE fin_employee_registry 旧八标签映射（采集/链上→数据、执行→交易、运维→运维、通知→IR，余同名）；roster 文件改写仍归 M2.6；⑥ **白名单**：api 部全向边按实引清单冻结（~35 边），trading→api 测试边（回补件）注明 test-only；⑦ **58 函数回补**：函数级取件勿整文件覆盖（D-223⑤），import `arb/internal/dashboard`→`arb/internal/api/dashboard` 机械改写，fixture_helpers 同归，回补后 autotrade 函数数=462 对拍。 | 放前把最重票的断点全裁清：§1「按§4」引文失效若不预裁，实施席必在监控组件归属上停手；hedge 剔除以字段级证据定族件属性，不留「看着办」。 |
| D-226 | 2026-10-12 | **M2.5 复审 R1 裁定（决策席）：退回，四项补齐后重报**。机检独立重跑全绿——build/vet=0、gofmt 空、import 方向 210/210、`go test ./...`=62 ok/85 包/0 fail、race（api+autotrade+gate+lifecycle）=1312 过/6 包；`TestBinanceUserWSE2E`/`TestD478` flaky 判词采信（隔离绿、M2.1 搬入件非本票面）。**退回项**：① api/dashboard 下 98 个 `dNNN_` 文件未改名——入场门明文+M2.1–M2.4 全执行先例，无豁免；② `TestD423_EntryGateFailClosed`/`TestD423_ReverseComboRejected` 未回补（仍在 d423_engine_part.txt.txt，报备「已并入」不确；fixture 已全在仓可编）；③ D-225④ 留面 107 RPC 分组消费者清单未产（删面 8 举证已足）；④ 残注三处（service_ctor SetCmcLiveProvider/data_refresh subscribeCMCWatchTopic/harvest 孤儿注）。**追认不返工**：pgstore/cmc.go+cmc_query.go+hedge_flow.go 提前剔（baseline 本无 fin_cmc_*，M3 票行核销）；intra RPC×2+ops.proto 消息剔除（不搬族延伸）；alertInserter 孤儿救件；trx_yield 死成分剔除+binance tick 等效换源（注释留证）；4 件 graduation merge-loss 回补逐字一致；+1 gate 伴随测试（文档在件）；stream 字段号 reserved 留号；go.mod 零 diff 合理；`m25_suspended/` 留档裁=回补验收后 README 加「回补完成 D-226」转纯史料。 | 硬闸三项（改名门/零静默丢失/举证面）是搬迁工程仅有的三道一致性护栏，机检绿不能替代票面；追认项全部有字段级证据与出处注。 |
| D-227 | 2026-10-12 | **残留文面瑕疵挂账（决策席）**：`internal/trading/autotrade/harvest_net_negative_test.go:94` 两行孤儿注（描述已并入 `entry_gate_fail_closed_test.go` 的 D423 函数）未随 R1 清——纯注释残壳、零行为影响，不够成二次退回；**转 M2.6 装配票顺手清**（同一文件的同批触碰面内），期间保留不算漂移。 | 复审成本纪律：四项硬闸已全过，为两行注释再开一轮 R2 不抵迭代成本；但孤儿注属「描述不存在的函数」文面错误，必须挂账不能静默。 |
| D-228 | 2026-10-12 | **M2.5R1 复审裁定（决策席）：通过，已合 master（ff `35bde92`）**。D-226 四项退回逐条实证：① `dNNN_` 文件名 98 件全改名，`find internal -name 'd[0-9]*_*.go'`=0，diff 为纯 rename+出处头（`service_carry_comparison.go`/`appconfig_rpc_test.go`/`d036` 系抽查逐字等价）；② `TestD423_EntryGateFailClosed`+`TestD423_ReverseComboRejected` 已回补 `internal/trading/autotrade/entry_gate_fail_closed_test.go`（函数级取件、新包路径、出处头规范），三包函数名集=归档 462 全集+1 gate 伴随件（D-226 已追认）；③ 107 保留 RPC 分组消费者举证落 `docs/handoff/m25_rpc_audit.md`（8 删+107 留=115 闭合，飞书/ops 走 pgstore 直读不入 RPC 面已明记，107=即 M2.6 挂载面）；④ 残注清（service_ctor/data_refresh/positions 三件，diff 负增量）。机检独立重跑：build/vet=0、gofmt 空、import 方向 210/210、`go test ./...`=85 包绿（api/dashboard 全量 781 绿；`TestD587_ReadyWaitKindText` 全量并行负载下单次 flaky、隔离 3/3+整包复跑绿，判环境抖动非代码缺陷，与 D-226 已采信 flaky 同型）；R1 增量=纯 rename 零内容差（`| 0` 行级），race 沿用 bd53044 基线 1312/6 包净+回补件 D423 单测 race 6/6 净。报备②「m25_suspended 转纯史料」已落实（README 明记 58 函数回补验收+归档态）。**遗留**：D-227 孤儿注（M2.6 顺手清）；pgstore cmc 早剔已追认，M3 票行核销口径不变。 | 四项硬闸=搬迁工程仅剩的一致性护栏，逐项实证后通过；flaky 判定有隔离+整包复跑双证据不靠猜。 |
| D-229 | 2026-10-12 | **M2.5b 放前裁定（决策席，设计先行票）**：① proto 面无需扩——`Employee`（employee.proto:7-19）已含 dept/assembly/cadence/running/exits/task/beat_ts/status（在职\|滞跳\|离线\|离职），公司架构页字段零缺口，`ListEmployees`+`StreamUpdates` 即足，**禁本票新增 RPC/proto 编辑**；② 设计稿=`docs/modules/web.md` 一页（≤100 行）须含：页面清单×逐页 RPC×StreamUpdates case 消费表×组件拆分（≤300 行/文件）×流订阅单例模型×NOX 露面点（登录页/投资人页抬头/报告落款，内部路由与组件名仍 arb 语义）——决策席批后方准写代码；③ v1 页面范围建议线：运营看板=总览/持仓/账户管理/风控健康/公司架构（新）+投资人页 v0（只读 NAV/收益/持仓摘要，NOX 品牌）；归档页清单仅作参照面（Overview/Brokers/Positions/CexScan/CexFinance/Finance/Trx/Knowledge/Reports/Settings/Login），砍页逐条举证不写即删；④ `web/` 现状=Vite+React19+TS 空脚手架+buf.gen.yaml TS 产出 `web/src/gen` 已通——零地基工程直接施工；⑤ 归档 `web/src` 仅读参照**禁整件拷贝**（D-213 重写令），样式纯 CSS 重写。 | 设计先行的意义=把「砍哪些页/数据从哪来」变成可审文档而非代码里的事实；proto 面预核堵死「顺手加 RPC」的逃逸口。 |
| D-230 | 2026-10-12 | **M2.5b 设计稿批（决策席）+ 两处裁量**：① **employees refresh topic 准接**——case14 `data_refresh` 为既有通用通道（空载荷+topic→前端重取），新增 `employees` topic 常量+心跳写点调 `PublishDataRefresh`，proto 零改不违 D-229①；架构页在线状态走流非快照，符合 D-004；写点若在 M2.6 装配域则页面先行上线、接线后自动点亮（如实 beat_ts 展示=正确 fail-soft）。② **投资人页取既有只读面准**——v0=业主自用读面，`ListDailyIncome`/`GetPnlAnchor`/`ListPositions`/`GetFinanceOverview` 足用；专用 NAV RPC=M4 NAV/配置器行域，外部投资人开放时才需窄面。**改名排序令**（业主令确认）：CI 修复票 → **改名原子票**（代码层 module/proto/env/测试DSN，`arb`→`nox`，映射表决策席钉）→ M2.5b 施工+M2.6 装配（全在 nox 口径写零返工）→ M3 切流折入运维面改名（路径/域名/DB/桶）。 | 裁量皆取「最小机制复用」：通用通道加一个 topic vs 新增 case，既有只读面 vs 开 proto 编辑口；改名排序按 churn 成本数学——wire 件是全仓 import 密度最高面，必须先改名后装配。 |
| D-231 | 2026-10-12 | **改名票实施裁定（实施席报备）**：① 测试库基名 `arb_*_test`→`nox_*_test`，**pg user 保留 `arb`**（票文二选一取后者——role 属运维面 M3 域，本票零 5434 角色变更）；② `app_config`/`app_secrets` 存量 `ARB_*` 键经迁移 `1002_env_keys_nox.sql` 改名（冲突裁决 NOX_ 已存在则删 ARB_ 旧行），历史 replay 迁移文件不动；③ 保留域值：`exec.OrderComment="arb"`/`is_arb`/`t-arb`/`arb-N` 订单标记族（券商侧历史订单与 CEX clientOrderID 前缀匹配语义，改名即断 reconcile 识别面）、TRON `ArbPermissionId`（多签权限协议语义）、analyst「arb 库」描述（实指生产 DB 名）、`tools/arbkb`→`tools/noxkb` 随迁（pre-commit 门禁调用点同步）。 | 键名走迁移而非运维手工——鲜装/切流两端自洽；订单标记是协议面不是品牌面，留着是证据。 |
| D-232 | 2026-10-12 | **改名原子票复审（决策席）：通过，已合 master（`ecdf8e66`）**。机检独立重跑：build/vet=0、gofmt 空、import 方向 210/210、`go test ./...` 全绿（api/dashboard 781、`arb` user 保留下 `nox_*_test` 建库/迁移全链含 1002 重放验证）、race 触面（api+auth+trading+risk 9 包）净、web npm build 绿。改名完备性：`"arb/` import 残留=0；`NOX_*` env=263=原 `ARB_*` 全数 1:1；`arbkb`→`noxkb` 含 pre-commit 钩引用同步；AGENTS.md 系统名/契约改。残留 `\barb\b` 逐条裁：订单标记 `OrderComment="arb"`/client order id 前缀/`arb-uds`——**venue 侧连续性保留**（在仓订单带 arb comment，改名会砸 is_arb 检测）；`arb`/`arb_ro` 生产库名+role 引用——M3 运维面收口域；`"arb analyst"` prompt——域词非品牌；测试夹具 comment "arb" 随 OrderComment 留。**实施报备追认**：D-231① pg user 保留（运维面正确分层）② 迁移 1002 冲突裁决 NOX_ 优先删 ARB_ 行（幂等+新键优先，正确）。**遗留**：CI 修复票在途（normalizeDSN 剥 pool 参+三 DSN 变量补齐——改名后 env 名已换 NOX_，修复同口径）；M3 运维面改名清单=路径/域名/生产 DB/docker/r2/cron。 | 原子票单笔 ~1150 文件零语义漂移，抽查非 import 增量仅为 env 名字符串本身；测试初挂（D543/settlement）经核为并发双跑污染测试库所致，清库后全绿——评审教训：全量套禁并行同实例。 |
| D-233 | 2026-10-12 | **全新项目身份令（业主令）**：M3 切流后 NOX=全新项目，编号体系整体重启——① 决策账本重开新序列（现 D-001~ 序列随 M3 封存为切流前历史，新票/新裁定自此重新编号，引用旧条目写 `旧D-xxx`）；② 订单标记 `OrderComment`/`arb-*` client id→`nox` 系（**M3 切流清单必须含过渡期条款**：切流时在仓 arb 标记单的 is_arb 识别须双前缀覆盖至清仓完毕，或切流前置清仓归零）；③ 文档/票据/请求 id 格式同换。开发机侧持续推进不变；本令只约束切流后表面，不回溯改历史。 | 业主品牌域+编号域固有裁量；过渡期双前缀条款防止切流当口在仓单失联（venue 侧标记不可回溯改）。 |
| D-234 | 2026-10-12 | **M2.5b 复审：通过**。七页（总览/持仓/账户管理/风控健康/公司架构/投资人/登录）+StreamUpdates 单例分发+dataRefresh topic 集重取；proto/gen 零动、无 setInterval、页面 RPC 全落批稿清单、全文件 ≤207 行；accounts 21 写径=批稿第 19 行授权域；org 页 employees topic 消费正确+未接线如实 beat_ts。机检：build/vet=0、gofmt 空、npm build 绿（tsc+vite）、dashboard 781 复跑全绿（D543 系残留库污染、清库后过；600s 压线超时为环境项非挂起——后续该包跑测须 `-timeout 1200s`）。遗留：embed 挂接/活后端端到端=M2.6 域；cut 页 v2 回补挂账。 | 复审裁定 |
| D-235 | 2026-10-12 | **M2.6 R1 退回四项+一裁**：① `78d7be62` 外来 DAG 嫁接（第二父 d8baf075=旧仓历史线）禁入 master——重挂单父，取归档文件用 `git show` 禁 merge 旧仓；② roster dept 旧八标签原样搬——RegisterEmployee upsert 会打回 1001 已归一行，按映射改字面量（采集/链上→数据、执行→交易、通知→IR）；③ 4 名裁汰员工（intra×2+cmc×2）注册在册且装配文件不存在——删；④ ci.yml `go test` 缺 `-timeout 1800s`（dashboard 781 测 ~10min 必撞 600s）。另挂 M2.5b 漏检：org.tsx DEPT_ORDER 与 EmployeeDepts 不一致（前端「决策/实施」应为「运维/合规」）。**裁：hedgeTopic 保留**——Publish 挂真实 fingerprint 事件、签名稳定、case17 消费面回设时零成本点亮；空发 ping 无害，动签名不值。 | 复审退回裁定 |
| D-236 | 2026-10-12 | **M2.6 R2 退回首项**：名册 `Assembly` 字段 8 条指向旧仓文件名（wire_engine/sched/recon/ticker/binance/okx/funding/carry）——新仓 wire 面已合并，真装配点=main.go/wire_posmon.go/wire_quotes.go/wire_scan.go；goroutine 43/43 真接线实证（safego 实名对拍零缺），纯元数据修但架构页装配点列须写真值。R1 四项已全部核验过：graft 单父化、43+十一标签、CI 四件套、org 页 DEPT_ORDER=EmployeeDepts。 | 复审退回裁定 |
| D-237 | 2026-10-12 | **M2.6 R3 退回**（CI 首绿验证 run 38231103517，前半程全过至 gofmt）：① `TestD341_CycleRealrateView` 裸 `pgstore.New` 不走 `testStore`/`s.Migrate`——本地靠遗留已迁移库绿、CI 空库炸（`positions` 不存在），改带 Migrate 助手自闭环；② `TestTestDSN_EmptyDSN_DefaultsToStandaloneInstance` 测缺省 `arb:arb` 路径而 CI postgres 无 `arb` role——ci.yml 加建角色步（`CREATE ROLE arb LOGIN PASSWORD 'arb'`），不动缺省 DSN 契约（D-021）。存疑待验：测试步 58s 早终~20 包无结果（dashboard 10min 级未跑），疑 OOM/驱动被杀——修后仍发加 `-p` 限并发排查。 | 复审退回裁定（CI 首验暴露潜伏测试缺陷非票回归） |
| D-238 | 2026-10-12 | **M2.6 R4 退回**：同型潜伏 10 件全清（实施席举证属实）——`dashboard×7`（broker_rpc_not_found/cex_executor_demo/mt_tradability_probe/service_pending_entries/swap_diag_probe/symbol_seed/wait_condition_exposed）+`pgstore×1`（broker_setters_not_found）+`gate×2`（gate_margin_capitalview/margin_warn_throttled）裸 `pgstore.New` 无 Migrate，CI 并行调度下随机红；修法同型=换 `testStore`/`SharedStore`/storetest 带 Migrate 助手。判据=CI 稳定绿非一次绿。 | 复审退回裁定 |
| D-239 | 2026-10-12 | **M2.6 复审：通过**（R1 graft 单父化/R2 Assembly 8 条/R3 CI 潜伏测试/R4 同型 10 件四轮后收口）。终态核：30 wire+7 测、名册 43 十一标签全指实文件、43/43 safego 实名对拍、embed 挂接、tools 余件（digest 37 测绿）；裁汰族零引入、hedge 域整族退役一致（KickHedge 下游已随 M2.5 除，posMonC.hedge 不搬正确）。CI：run 38233372571 全程绿=前置件实证；R4 二跑 postgres 服务级故障（broken pipe→stall 至 1800s 超时）判 infra flake，同代码 R3 绿+R4 仅测试件 Migrate 三行增且本地触面全绿。**教训入库**：①禁 merge 旧仓取件（git show 读文件）；②测试自闭环——裸 New 依赖遗留库=CI 空库炸弹；③本地 10min 套件在 CI 干净机秒级，勿以本地时长推 CI 时长。 | 复审裁定——M2.x 全线收口，剩 M3 |

| D-240 | 2026-10-12 | **业主令：放弃 MT4**——§1 资产锚改「CFD（MT5）+ 加密（CEX/DeFi）」。生产实证 `broker_accounts` 5 账户全 `mt5` 零 `mt4`→无数据处置面。代码面单列票（不折入已报审 M3）：MT4 概念族=errclass/notional/combo 域值、config 键、`add_broker` 拒绝径、dashboard 服务件、`data_adapter` mt4 通道面及测试件；删前逐点查生产枚举/行值实证零引用，防硬编码域值误删。**时序裁定**：切流后独立票——M3 报审面不扩容，休眠概念切除不值冒切流窗风险。 | 业主令·方向锚变更 |
| D-241 | 2026-10-12 | **M3前置 R1 退回（七必修+三 nit）**：① `.dockerignore` 缺席——`COPY . .` 拉 `.git`(77M)/`web/node_modules`(宿主 glibc 原生件污染 musl)/`.env`(若在)进镜像层，归档有现成版照抄适配；② `Dockerfile.core` web stage 缺 `buf generate`——`web/src/gen` gitignore，本地能过纯靠 context 凑巧有件（CI 同型洞 D-239 已修，镜像径未修）；③ `scripts/paper_window_report.sh` `key='ARB_PAPER_WINDOW_START'` 残留——1002 后必 FAIL（真断点非词面）；④ `xasset-monitor-run.sh` `export ARB_FEISHU_APP_ID` 残留；⑤ `evidence_pos.sh` `ARB_MASTER_KEY`/`ARB_BINANCE_PROXY_TOKEN` 残留；⑥ `build_host_tools.sh` `ARB_BIN_DIR` 残留；⑦ **切流卷断层**：compose `nox-pgdata` 新卷 vs SG 现存 `arb_arb-pgdata`（arb-postgres 实挂）——票面 §8 无卷迁移步，`compose up` 起空库=静默灾径，须写明 clone（`docker volume create`+alpine cp）或 external 挂旧卷过渡。⑧ `nox_ro` 鲜装缺口：replay/0041+0047 造 `arb_ro` 正确（历史链不可改）但无件补 `nox_ro`+GRANT——生产靠切流 ALTER 覆盖，鲜装断，建议 1004 幂等补造或票面写明运维补建。nits：`wire_venue_close.go` `t-arb`→`t-nox` 过度改名（注释记 #505 venue 实值=历史证据应保 `t-arb`）、`dsn_guard.go` 注释「生产 db=arb」陈旧例值、`tools/cex-proxy/README.md` 词面。**机检**：build/vet/gofmt/行数/import210 绿；全量唯一 FAIL=TestD543 席位库污染（隔离复跑绿）；race 触面 7 包干净。**核过面**：①12 表对拍 §3 吻合+IF EXISTS+capital_snapshots 留、②cmc 零代码残留（注释/白名单属兼容件）、③IsOrderComment 双前缀集中判定正确、④subscriptions 惰性兜底+fail-closed 语义贴合生产病灶、migrate_table baseline/squash 语义正确、单父线性无外来 DAG、proto/gen/web 零动。 | 复审退回裁定 |
| D-242 | 2026-10-12 | **M3前置 R1 复审：通过**。① `.dockerignore` 补造正确（`.env*`/node_modules/`.git`/`web/src/gen` 全挡——后者强制镜像内 regen 确定性路径）；② Dockerfile buf 段设计+**`docker build --network=host` 端到端实证**：buf@1.68.3 pin→generate 0.6s→npm build 2.7s→83.5MB 成镜像；③ `buf.gen.web.yaml` TS-only 模板路径正确（local 显式 `web/node_modules/.bin/protoc-gen-es` 免 PATH）；④ 四脚本 ARB_ 清零、三 nit 全收；⑤ `1004_nox_ro_role.sql` 幂等可重放（DO 块+REVOKE/GRANT，列名 fresh-replay 测试过验）；⑥ §8 落卷克隆步（`docker volume create`+alpine cp 命令或 external 备选）+compose 注释锚；⑦ 新运维事实：`--network=host` 钉 AGENTS 部署行（宿主 mihomo 代理 vs docker NAT 裸出——npm ECONNRESET 实证）。**遗留裁**：buf regen 镜像内已实证，无遗留。 | 复审裁定——前置码件收口，剩闸+切流（本席执行） |
| D-243 | 2026-10-12 | **M3 切流执行（业主令窗口）**：§7 闸②③④⑤⑦全过（函数级对拍 150 缺件全归族/名册47-4=43精确差/RPC 115-8=107/SG 95行=81业务表全有属+7DROP+6基建/import210）；闸⑥影子 44min 零 critical+迁移 1000-1004 实战彩排全过；闸①全量套判=dashboard 负载敏感超时 flake（D-234 同型，独立 21.8s 774 测全过）。切流实序：停 arb-core→`/opt/nox-backups/d212-archive/arb_pre_m3_*.sql.gz`(245MB)→停库→卷克隆 arb_arb-pgdata→nox_nox-pgdata→起 nox-postgres→`ALTER DATABASE/ROLE`→TRUNCATE+1000→.env ARB_→NOX_（MASTER_KEY 原值+proxy 键不落）→nox-core 起 env=live→cron 六行全换→cloudflared-arb→nox（同 token，域名路由在 CF 面=业主件）。**切流实证修正**：`dciSubscriptions` 端点 `/v1/dci/product/subscriptions` 实拍 404 不存在——改证 `positions?status=SETTLED`（credread 签名实测返 #7 行 id=18537557）；匹配键真值=订单 `response.positionId`（product_ref 是产品号非仓位号）；`settleAsset` 交割键优先于 exercisedCoin（#7 未行权 PUT 到账=USDT 非 BTC；利息=settleAmount−subscriptionAmount=1.4066）。**#7 生产已翻 settled+账面补正**（settle_ccy=USDT/interest=1.4066/income_event tran_id=71）；#8 settleDate 明日未到期。回退备件：arb_* 容器 Exited 留存+旧卷+归档 dump。 | 决策席·切流执行+热修裁定 |
| D-244 | 2026-10-13 | **部门日报→经理摘要立票（业主令，含财务专项）**：闭环「复盘进化」段补综合层——十一部日频 LLM 部报+账本部专项财务日报→经理摘要→飞书/看板。定界：LLM 输出仅观察+建议面，零入口于风控/执行/淘汰路径；数值全部 PG 算定，LLM 禁编数；复用 analyst.Client/Tools/RateLimiter+alerts 扇出，~12 调用/天成本可忽略。 | 业主令·架构补强 |
| D-245 | 2026-10-13 | **部门日报→经理摘要复审：通过，已合 master（`7806a904`）**。机检独立重跑：build/vet/gofmt 空报、import 212/212 边、deptreport+pgstore+analyst 298 测绿、race 4 过、TestListDeptReports 过。核过面：gather 全确定性（PG 渲染文本喂入）+双系统提示禁编数；manager 摘要只读落库部报正文；fail-open 全链（单部失败续跑/无 AI 配置当班跳/飞书缺件只留痕）；07:05 BJ 日频+重启不补班（防 LLM 风暴）；迁移 1005 幂等 PK(dept,date)；roster 登 main:deptreport cadence=86400。**决策席补正二件**：①Deps.Beat 每班心跳钩（Run fn 内睡整天，仅启动拍则 3×cadence 后滞跳假阳性——名册假象同类病灶）；②gather.go 注释旧词归一。**遗留裁量放行**：无专属前端页（ListDeptReports 备页面回补消费）、aiRate 共享桶（12/天 可忽略）、usage 缺事件=token 列诚实 0、日频周期实证随部署验。 | 复审裁定 |
| D-246 | 2026-10-13 | **公司架构审计裁定（决策席，业主令「审计并可推翻」）：v2 推翻隐喻层，`docs/公司架构.md` 改 v3**。生产实拍：`fin_employee_registry` 143 行中 95 行未登记/dept 空（ws-ping/keepalive/子循环）、投资/运营/复盘/合规四部零员工；12 次/日部报输出主体=考勤并把不搬族尸体行报成离线员工；7 策略 6 禁用、OPEN=0、income 自 9/22 合计 14.95 USD；`fin_proposal` #1 APR 788.6% 挂 proposed 两天；`capital_snapshots` 已于 0180 DROP 而 v2 四处引为 NAV 载体；KPI 代码零命中。**裁**：保留核心对象/钱的旅程/三红线/钱径零 LLM；组织改「钱径六域（数据/研究含运营/投资/风控/交易/账本含复盘）+ 平台横切 + 一个 LLM 判断工作者」，**目录不动**（路径保留，语义归并）；花名册→daemon 台账（Beat 不建行、子循环不入册、尸体行删、Dept 7 标签）；11 部报→1 次 money-first 公司日报 + `fin_llm_call` 调用账本入 D-013 费用口径；NAV 载体改新表 `fin_nav_daily`；纸面沙箱写权归研究。「1 部门 1 agent+LLM」否决于钱径（非确定/秒级/不可回放/职责分离不存在），采纳于思考面（五岗=一 client 多 prompt）。 | 部门=写权边界成本为零，隐喻层（员工/考勤/KPI/部报/架构页）才是成本且已实证产生误导面与投资人接触泄漏；$800/年利润空间容不下组织仪式。 |
| D-247 | 2026-10-13 | **投资人接触面硬规则**：① 到达投资人的只有「请求」（条件/资金，三要素+可行性预检：占用 ≤ 资金基数×单策略上限且 venue 在能力账本内，否则模板休眠不发请求）与「报告」两类；② `fin_condition_gap.kind` 白名单 `credential|permission|capital|constraint|holding`，**`runtime` 禁止**——存量 4 行（#19/#22/#24/#25「系统侧病灶」）转 STATE 票并 waived；#10（okx 期权需权益 ≥10000 USD=整个基金）预检不过→休眠；③ LLM 输出含「建议业主/业主面」类句 scrub 拦截；④ v2 §12「部门新增/合并须业主明示」废止——业主域仅方向锚/硬线数值/真金/凭据权限条款（AGENTS §1/§3、D-184），其余 D# 自裁。 | 生产请求箱 7/4 为系统 bug、1 为不可行请求=D-184「条件缺口→业主请求」被滥用为「问题→业主」；决策席上一轮亦照抄 §12 向业主请示组织变更，条款本身是泄漏源。 |
| D-248 | 2026-10-13 | **M4 路线翻转 + 文档事实卫生**：票序改 M4.0 运行面降噪+请求箱分流+日报 money-first+LLM 调用账本 → M4.1 验证器+评估卡 → M4.2 生命周期+晋级闸+提案 SLA → M4.3 风控单入口 → M4.4 NAV 日切(`fin_nav_daily`)+配置器 → M4.5 复盘员 → M4.6 投资人页 v1（v2 把复盘员列第一，但 OPEN=0、CLOSED 不再产生，无标的可复盘；断点在 G2/G4/G5）。搬迁清单 §3 加注：`capital_snapshots`/`paper_trades`/`ledger`/`cot_position`/`fred_observation` 生产不存在（0180 DROP），D-241「capital_snapshots 留」失效。D-233① 编号重启**追认不执行**（D-244/245 已续号，续号比重启更可引用；D-233 ②③ 不变）。 | 路线按「哪一段让 8% 不转」排，不按「哪件最便宜」排；文档引用的表必须在 pg_tables 存在或标新。 |
| D-249 | 2026-10-13 | **公司组织面开源模式引进四件（业主令，复盘进化段补强；原编号 D-246 与同日架构审计撞号，让位续号 D-249）**：调研四参照系（TradingAgents 基金公司式七角色+多空对辩、MetaGPT `Code=SOP(Team)` 角色契约、CrewAI role/goal/backstory 职责三元、Magentic-One Orchestrator 双环账本）——框架本身不引（Python 栈+LLM 坐决策位违 D-004/审计原则），仅引组织模式四件，按 v3 审计裁定（D-246）重述：①策略淘汰多空听证（判定权留确定性函数，LLM 只留对辩证供复核——独立于部报层，与 v3 无冲突）②日报待办闭环（fin_dept_reports 跨日对拍，重定向到 M4.0 单日报 dept='company'，前置=M4.0）③员工职责书 goal/constraints（**D-250 裁弃**）④日报 SOP 契约（重定向到 M4.0 单日报 money-first 固定序段校验，前置=M4.0）。统一禁线=全部 LLM 输出仅观察/建议面，零入口于执行/风控/淘汰判定。 | 业主令·组织模式引进；②④ 依 D-246 v3 裁定重定基；③ 由 D-250 收口 |
| D-250 | 2026-10-13 | **v3 审计复审（业主令「按总纲去留由复审裁」）：D-246/247/248 全案照准 + D-249③ 弃**。取证（SG psql×2+代码通道交叉）：名册 143/95 未登记 dept 空四部零员精确命中；12/12 部报行含考勤词+manager 正文实证「IR部建议业主面披露」泄漏（D-247 scrub 规则有据）；strategy_stats 6×disabled+XASSET_CARRY pnl=−63.477、positions 12C/2F/OPEN=0、income 9/22 起 14.95 全精确；fin_proposal #1 apr=788.619 挂 proposed 两天、requested 7 条 runtime×4（#19/22/24/25）、#10 权益 1 万门槛在录；capital_snapshots/paper_trades/ledger/cot_position/fred_observation+新表四张 pg_tables 全不存在；KPI 代码零命中；`EmployeeBeat` 内 INSERT 实锤=95 未登记行成因。小差一处：「3 条 semi」实=2 条 semi+余轨挂起合计 5 行 proposed，方向不变。**③裁弃**：v3「人数=0」下 daemon 职责已在装配码+§4.1 域表，purpose 列=仪式回挂逆裁定；原案锚「部报答岗责」随 11 部报废止已死；上轮「待定业主裁」即接触面泄漏，本席自裁收口。观察备录：`strategy_stats.disabled_reason` 存量含「判死禁用」历史数据值（术语面另票清洗，不动历史行）。 | 复审裁定 |
| D-251 | 2026-10-13 | **复审席扩权定档（业主令）**：复审职责定三件套——代码核验（原职）+**方案核验**（架构级产出落地前第二眼，范式=D-250，§4 闸先行于施工）+**最优解审计**（核「最优合理解」非「能跑」：新造轮子/该删未删/半截实现/判据硬编码/有流轮询/占位充完成=偷工退回）。**档位**：钱径（风控/执行/账本/仓位/淘汰判据）与架构级方案复审=最强可用模型新开会话（金融工程 effective challenge——校验能力≥实施，有权否决）；观察面=平级。会话独立约束不变，模型档=放大器非替代。 | 业主令·复审扩权 |
| D-252 | 2026-10-13 | **首席→次级决策席交接定档**：M4 期派单与验收依据=STATE 首段「首席交接」（席位权限、硬派单序、放前门、四道验收门 G1 事实/G2 契约/G3 最优解(D-251)/G4 效果、逐票 SG 实拍验收数、30 天验收线、回首席触发）。次级在 v3 框架内全权派单/复审/验收/D# 续号；六域写权、三红线、D-247 接触面、D-248 序、§4.3 六指标、钱径零 LLM 为首席保留项，改须 `[角色:首席决策]` 新会话附实证。一票未验收不放下票；M4.1–4.6 票面由次级起草并先过 D-251 方案核验。 | 首席定结构与判据，次级定节奏与验收——两席分离让「方案最优」与「执行合格」各有一道独立的眼睛 |
| D-253 | 2026-10-13 | **M4.0 放前门方案核验：通过，放行实施席**。核四件：①票面生产事实 D-250 两通道取证在档；②补述(c)消解——`analyst→pgstore` 已在 import 白名单（脚本现行边+tools.go import），落行点可在 platform 层零新边；③95 行未登记逐名裁决种子表入票面（退役删5/子节拍不入册24/一次性Go~12/补册真daemon~30/父册覆盖~24/待核3族——实施以 Go/GoLoop 调用点证据增删±）；④**验收数修正**：「≈48 行」改「编制终局=裁决表数（种子≈78）」，终局≥80 行自动触发回首席。**序裁决**：D-249①②④ 不前置不并行施工——按硬派单序落 M4.4 后与 M4.5 同槽（①赖 fin_llm_call 入账、②④赖单日报结构）；并行独立票仅 DCI 轨 4 件+MT4 退役。领票序行已对齐 D-252。 | 次级决策·放前门+序裁 |
| D-254 | 2026-10-13 | **`engine:meanRevertLoop/swapArbLoop` 禁用策略 loop 存活核实=刻意设计「复活雷达」，非病灶，裁决表转正补册（交易）**。证据链：①两文件同注「判死策略停扫改为停提交——扫描+评估常开喂 rollup」+swapArb 注「确保 disabled 策略仍被扫描评估（雷达有人值守）」；②`IsStrategyDisabled` 只拦 `gate.Submit` 提交，不落 REJECTED；③`maybeRevivalAlert`=禁用策略净 bps 达阈值→critical 告警提请复活复盘；④生产实拍 swapArbLoop 今晨 01:52 全扫 14 组候选（全 product group mismatch 跳过=当前无合格跨账户 swap 对）。含义：禁用=关扳机不关注视，死策略市况回春时系统自提请——正是复盘进化域机制，入册后健康可见。 | 次级决策·核实 |
| D-255 | 2026-10-13 | **venue 败因坑表立案（业主令「失败应进坑库避免反复查询」）**：`fin_dci_order` #1–6 六连撞取证——-1102 参数名错（projectId/lots→id/orderId/depositAmount，leg.go 注释 R10① 即此坑修复痕）→-9000「Enter an amount to 0 decimals」整数要求→-9000「APY updated refresh」报价过期，三连坑递进、同一意图 100min 内反复撞 venue。病灶三面：败文躺 `fin_dci_order.response` 死字段零组件回读；reqID 金额浮点尾破幂等（每轮新键）；`errclass` 仅 MT4/MT5 码表、CEX 兜底 Abort 且 DCI 径不消费。立案：`fin_venue_reject` 机器可读坑表（能力账本补件——约束业主喂、坑 venue 教）+errclass CEX 码表消费点入 tryLeg 申购前查坑（自修复或抑单+一次性 critical）+reqID 意图稳定键。落并行槽；与 DCI 轨 4 件分工=彼修具体病灶、此建防再撞机制。 | 次级决策·立项 |
| D-256 | 2026-10-13 | **M4.0 报审受理+82 行编制条款消解（次级裁）**：施工报审 @341bbb57 四块分提交合规受理。票面「终局≥80 自动回首席」条款消解——编制表 44→82 逐名构成核（engine 六 loop/cex-stream×3/mt5×5/trx+wire_trx×5/selfwatch×3/平台收尾全为种子表明指族/补册/待核转正面），种子 78→实测 82=+4 漂移且抽查名件均可追调用点，无注水件；条款目的=防编制回潮非死数字，构成有据即达。消解以复审 G4 台账逐项对拍为闭——若无证件出再升级。遗留①SG 四项实拍归复审部署段、③feishu_query CallLog 接线归复审 diff 审。 | 次级决策·受理+条款裁 |
| D-257 | 2026-10-13 | **M4.0 终审席审计：通过，合并 `b9b1dbf7` 部署 SG**。四道门：G1 事实=四块 diff 通读+机检独立重跑（build/vet/gofmt/race317/web 全绿）；G2 契约=钱径零 LLM/零新 env/setup.go 恰 300 行/迁移 1006-1007 登记嵌入/EmployeeBeat 只 UPDATE/子节拍 GoPlain 转换面全配对/CallLog 8 消费面+2 继承径全接线；G3 最优解=deptFocus/genManager/compact 死面清零、InsertAlertDedup/NetRealizedSince/DormantPairs 全复用零新造、CapitalBaseUSD cfg 驱动非硬编码、Beat 注入解 86400 滞跳。**审计修正 1 件**：D187 断言漏跟进三通道产出面（gap/提案/休眠约束）——冷库全能力缺失全走休眠=设计内，原断言误报静默病灶，补 dormant 计数 `5b7853d6`（冷库删库实证过）。G4 实拍：台账 82 行=编制表精确对拍（多出 1 行 `probe-report`=GoPlain 一次性件旧语义残留幽灵行，running=f+beat_ts 空实证手术删行）；请求箱 runtime×4 全 waived+D-247 转票；dept 零空；main:deptreport running+t 心跳新鲜；`fin_llm_call` 表已建（首行随自然 LLM 调用落——explorer/feishu/company 三面已接线）。观察项：fin_llm_call 首行+company 首报今夜 23:05 UTC 落；`feishu_query:handle`/`main:cexProxyHealth`/`service_snapshots:purge-acked`/`setup:mt5-quote-fwd`/`trxvote:confirmSelfTx` not_running=事件驱动/条件装配/长 cadence 未到拍，如实显形非病灶；`main:trxvote` exits=41+running=f=退出模式台账显形（既有行为非本票引入，值另票查）。 | 终审席·审计通过 |
| D-258 | 2026-10-13 | **M4.1 放前门方案核验：通过，直派实施席（终审席兼）**。放前实证二件闭：①#1 `funding_carry`（auto/788.619）滞留=**auto 轨标签已立消费面缺如**——`gate.ExecTemplates` 仅 DCI 两模板，无 auto 消费件，feishu 已标「（无执行轨）」=非闸故意拦是根本无闸，788% 两天未评估=验证缺位实证（v3 §20 行同款结论）；②semi 轨=有径（提案列+go N→GoProposal）但执行面仅覆 DCI 两模板，semi 模板批准=「未接执行轨」如实拒单。票面定 v3 §7「验证器+评估卡」原义：econ 升格模板级（净 APR=毛−费−滑点−失败成本−窗损，无证据→unevaluable fail-closed）、`fin_opp_eval` 一提案一卡、无卡不算候选、执行后回写喂指标⑤；生命周期/晋级/提案 SLA 归 M4.2 不越界。核验：纯研究域评估面零执行权、确定性 Go 零 LLM、零新 env、单迁移续号、目录不动。 | 终审席·放前门+直派 |
| D-259 | 2026-10-13 | **M4.1 终审席审计：通过，FF 合并 `67a42159` 部署 SG**。G1 事实=diff 通读（econ 纯算 63 行+oppmatch 装配 87 行+pgstore 135 行+exec/settle 闸与回写）+机检（build/vet/gofmt/触包测试/race 全绿，全套件 64 包过——唯一 FAIL=TestD587 FUNDING_HEDGE dial timeout 同 M4.0 infra flake 签名，隔离 3 passed 排除，连发两次值记档）；G2 契约=零 LLM/零新 env/全件≤300/迁移 1008 登记/import 边白名单增 oppmatch→econ；G3 最优解=卡闸位置在 ExecTemplates 判定前（通用先于模板）且 Go() 裸口令同径零绕行、actual_apr 回写仅本息同币（行权异币如实跳，归复盘票）、mechanism:不适用证据项=确定性模板语义非外推、percentile_cont 同型复用零新造。G4 实拍：迁移 1008 顶头、`fin_opp_eval` 首拍即落 5 行——#1 funding_carry 516%→**unevaluable**（fee/slippage/failure 缺证据，票面「788% 类→unevaluable」验收语义活实证）、#3/#4 理财 failure 缺证据、#5 五项全缺、**#121 dci_buy_low→viable net=20.23**（go N 径通）。遗留确认：②存量卡=首拍即出实证关闭；③行权接货 APR 异币口径归复盘指标票；④funding_carry/earn 可评化待证源票。验收债务（不阻塞）：feishu 提案列未标 verdict 显示面。 | 终审席·审计通过 |
| D-260 | 2026-10-13 | **M4.2 立票+放前门核验：通过，直派实施席（终审席兼）**。放前实证四件：①存量厚件清——live_gates 确认表/carry_graduations 档/elimination.go/strategy_stats.disabled 在，缺=统一状态机（布尔载不动六态语义）；②`elimination.go` 三段判据=代码字面量（floor30/胜率0.3/median+avg双负）直违 v3「判据全机读禁字面量」=硬缺口；③提案 SLA 缺位=#1 挂两天+5 条 proposed 无人决策实证；④go-N 逐单批=bootstrap 形态（离散品等批过期 #6/连续策略物理不可行）→ 晋级后自动执行为票面固有范围。**核验**：auto-exec 三面边界钉死——ExecTemplates 白名单+评估卡 viable+现有约束闸（custody_share/tenor/AutoTradeMaxNotional）+投委会翻 state=live 才开火（首次真金权位不动）；state≠live 全投影 disabled=fail-closed；SLA 期限机读非字面量；钱径零 LLM、零新 env、迁移续号。M4.3 风控单入口/M4.4 预算器不在本票——自动执行面只吃既有约束闸不新增径。 | 终审席·放前门+直派 |
| D-261 | 2026-10-13 | **M4.2 终审席审计：通过，FF 合并 `c6de485e` 部署 SG**。G1 事实=三块四 commit diff 通读（迁移 1009/strategy_lifecycle 261 行/criteria 111 行/elimination 迁表/autoexec+wire/oppmatch lifecycle 三件并入）+机检全绿（build/vet/gofmt/触包/race/全套件 64 包零 FAIL——D587 flake 本轮未发）；G2 契约=零 LLM/零新 env（SLA 入 app_config 非 env）/≤300/迁移 1009 登记/import 边白名单增 oppmatch→lifecycle；G3 最优解=种子判据与原字面量逐项语义对等（[5,30)双负 all、≥30 单条 any、<5 无行不火）、判据每判实时读表=改行即生效、读败/空表→不杀+log（正确 fail 侧——读败误杀=全停策略）、demotion 零消费件=存面就绪无点火径（报审「机读面就绪」稍宽——存面在消费件未建，但空=安全侧结论成立）、IsStrategyDisabled/ListDisabledStrategies 投影=fs 非 live OR ss.disabled 并集双保险、SetStrategyState TX 内两表同写（旧 NOTIFY 触发器免改仍驱动引擎刷新）、EnsureStrategyRows stats→live/retired+模板→candidate 幂等、AutoExecLive 挂 wire 层合 research↛trading 红线。**审计修正 1 件**：`err.Error()=="no rows"` 字符串比对两处→`errors.Is(pgx.ErrNoRows)` 惯例对齐（`c6de485e`，allow_rules.go 三处先例）。G4 实拍：迁移 1009 applied 04:23:59；`fin_strategy`=16 行全覆盖（7 stats=1 live XASSET_CARRY+6 retired；首扫 `ensured 9` 模板键→candidate）；判据=elimination×2+graduation×1，demotion 空表；`proposal_sla_hours`=24；**SLA 首拍即火**——5 条存量 proposed（#1/#3/#4/#5/#121）→expired+critical 告警落行（#1 挂两天实证单、#121 viable 但超龄同翻——陈旧报价不可执行语义正确）；autoexec 全 candidate→n=0 静默（fail-closed 实证）；零 live 迁移告警（backfill 不径 SetStrategyState=正确）；proposed→终态<24h 结构性保证成立（新提案超龄必翻）；fin_opp_eval 5 卡管健。票级验收数逐项对拍全过。遗留确认：①demotion 判据空=安全侧存面就绪消费件未建；②paper 写径待委员会/probe 域；④键空间碰撞理论面已记（IsStrategyDisabled 消费面=引擎名非模板键现况安全）。 | 终审席·审计通过 |
| D-262 | 2026-10-13 | **M4.3 立票+放前门核验：通过，直派实施席（终审席兼）**。放前实证盘清执行面：正规径=`Gate.Submit→intent.Execute→pipeline`（autotrade 三 loop 已全走）；绕行面七处——两处 `pipeline.Execute` RPC 直调（meanrevert/internal，零 intent 零闸=最优先堵点）、三处 service 层 PlaceOrder 直放（demo/probe/手动 broker 单）、graduation_alloc_risk、dciexec GoProposal 自链（用 gate 包函数不径 Submit——腿异构不硬扭）。命名坑勘明：`cex_executor_gate*.go` 1712 行=Gate.io venue 适配件非风控闸，票面「自带闸删除」对象另行枚举。票面定 v3 §7 原义：下单入口全分类收口（过闸/命名闸/委员会例外三档），dciexec 内联链抽 `gate.CheckDCI` 归单包，预算一致性闸接口预留 fail-closed（M4.4 才消费）。核验：committee 手动件不硬堵明示例外；钱径零 LLM；零新 env；迁移如需续号。 | 终审席·放前门+直派 |
| D-263 | 2026-10-13 | **M4.3 终审席审计：通过，FF 合并 `fd5a2d40` 部署 SG**。G1=diff 通读（CheckDCI 40 行纯判定/预算闸 23 行/Submitter 6 行/ExecuteOpportunity 33 行改+13 标记面）+机检（CHAIN_OK；触包 gate/dciexec/autotrade 绿；dashboard 隔离 783 过——并行跑互踩共享库（报审已述已知型）+裸跑 600s 包级超时两波 infra 签名非本票；race 163+重跑全绿；全套件重跑 SUITE_OK）；G2=零 LLM/零新 env/≤300/import 边 dashboard→risk/gate 规范登记；G3=CheckDCI 五检面与原内联链逐条等价（序同拒文同，模板存在性已由闸保证后 legs 直取安全）、预算闸挂点 breaker 后 Execute 前=正确位（nil=不消费合票面「接口预留不消费」，FailClosedBudget=误挂安全侧）、手动面收口用同 Gate 实例（闸态/熔断/审计同账——接口断言接线零依赖向）、gated-body 标记面诚实（ExecuteOpportunityInternal/ExecuteMeanRevert 调用方核实全为 intent.Execute 回调无双闸）、13 入口标记六分类完备（每含下单径文件恰一标）、DCI 不扭 TradeIntent=正确裁量（腿=产品申购非订单对）、RiskGate() getter 零调用=报审已述观察点不阻塞。**部署路径例外**：本机代理出口全节点死（vps2-cf+全部订阅节点 delay 超时，curl github=000）——GitHub push 暂挂、Dockerfile 常规构建 Go/npm 拉包 EOF 亦死；绕行=宿主 `CGO_ENABLED=0 GOARCH=amd64 go build`+minimal Dockerfile 打镜像（web/dist 未变 embed 新鲜）→SG 部署成。G4 实拍：迁移 applied 05:28；13 标记六分类 SG 源实拍；三 engine loop+oppmatch 心跳 <21s；零 panic；手动 RPC 闸接线实证=engine 构造即挂同实例（TestM43 锚测三态覆盖拒单可见/放行触达/未接线 fail-closed）。遗留确认：①fin_strategy_budget 消费归 M4.4 票面内；②RiskGate() 观察点零调用；GitHub push 待代理恢复补推（merge 已本地 fd5a2d40）。 | 终审席·审计通过 |
| D-264 | 2026-10-13 | **M4.4 立票+放前门核验：通过，直派实施席（终审席兼）**。放前实证四件：①NAV 无日切无单函数——deptreport 逐池现加，投资人页各消费面同型现算，口径漂移隐患坐实，`fin_nav_daily` 缺如；②敞口 cap 两表（venue/canonical）=committee 手写→pipeline 读，预算面若吸收须保存量行写径改配置器（禁静默清值）；③`gate.BudgetChecker` 接口+挂点 M4.3 已埋（nil=不消费）——本票=真实现+wire 装配，缺席行/读败 fail-closed 合接口契约；④策略级预算表缺如（现况=全局 AutoTradeMaxNotional）。核验：NAV 单函数化是「口径单源」非新算法——三消费面改读同一函数；日切并入既有 daemon 拍零新定时器；钱径零 LLM、零新 env、迁移续号。 | 终审席·放前门+直派 |
| D-265 | 2026-10-13 | **M4.4 终审席审计：通过，`e96ec95e` 部署 SG（实施席直落 master 未走 wip——路径偏差已记档）**。G1=diff 通读（1010/nav.go 146/budget 130/deps+run 日切钩/setup 装配/IR 改快照源）+机检（CHAIN_OK；触包+race 全绿；dashboard 隔离 783 过；全套件 65/66——唯一 FAIL=D587/FUNDING_HEDGE bad-fd 第四次同型 infra flake）；G2=零 LLM（derive 纯函数）/零新 env/setup.go 恰 300/迁移 1010 登记/import +2 边规范；G3=ComputeNav 单源三消费面实收敛（deptreport 口径零漂移——本就读同池；IR acctMon→快照表读）、写口 real-only 过滤实证（scheduler:226 IsRealAccount+mt_frame realKeys fail-closed 双写口）、BudgetChecker 三拒支锚测（缺席/超额/放行）、boot 首切堵「预算未产→全拒」窗口设计正确、derive min(maxNotional) 有界、敞口 cap 表未被吸收保 committee 写径=正确裁量。**口径漂移 1 处（记档不阻塞）**：快照表含已移除 venue `62311223` 残行（USD +172.95）——写口 real 过滤只拦新增，移除户残行照计；旧 IR 径 join broker_accounts 会跳。生产 NAV=5420.58 含此 +3% 虚值——今日零后果（预算=maxNotional 绑定），委员会可定是否补「快照新鲜度/venue 存续」过滤（一行）。G4 实拍：迁移 1010 applied 07:35；`fin_nav_daily` 首行落（nav=idle=5420.58 BJ 日）；`fin_strategy_budget` XASSET_CARRY=4000（live=1/reserve=0 缺席约束→0 储备→min(4000,5420)=4000）；boot 首切日志 `[budget] 日切 nav=5421 idle=5421 live=1 预算落 1 行`；零 panic。遗留确认：①②全闭——live 行在+预算产行+闸armed；reserve_pct=0 偏大方向已披露（委员会要严可一行改 fail-closed）。观察项：BudgetChecker 只覆 Gate.Submit 径——DCI 腿走 CheckDCI 命名闸不径预算（票面未要求；custody_share 约束另在），DCI 预算覆盖值后票议。 | 终审席·审计通过 |
| D-266 | 2026-10-13 | **M4.5 放前实证核验（复盘员事件驱动判决书）**：①`position_reviews` baseline 死表实证——零 Go 写读面、SG 实拍 12 CLOSED/0 行=覆盖 0%；②`review.Run` 触发面洞——`ClosePosition` 调用面 8 处（service_execute×7/probe_close×2/setters×2/reconcile×3/settleadapter/engine_scan×4/engine_service/finalize×2/resume×2），挂 review 仅手动 RPC 一处——**自动平仓零复盘**是覆盖率病灶根因；③`posChange` 提交即发已备（positions.go×4+chain_tx+exposure_caps）——订阅单点=全平仓径覆盖，<10min 延迟结构性成立；④`exit_kind` 权威枚举在场（11 值直写）——死因不重建不反推，LLM 复盘员职责收为归因叙事+followup_action（schema 校验 fail-visible）；⑤`position_settlements` 12/12 覆盖为数字源；⑥生产构成：12 仓全 XASSET_CARRY，费 ~4.2/仓 vs pnl±3=费蚀主导（carry_death 1/legged_unwind 6/incident_close 2/other 3）——判决书首期即显形费蚀病灶。**自检**：本票零钱径 LLM（数字纯 Go）、零新 env（复用 analyst cfg）、followup_action 仅建议不自动翻牌（判据表独占淘汰权）、posChange 订阅非新定时器。 | 决策席·放前核验 |
| D-267 | 2026-10-13 | **M4.5 终审席审计：通过，`4047f763` 部署 SG**。G1=diff 通读（posreview 105+87+111 行新包/pgstore 87/roster+wire+import 边 4 行）+机检（build/vet/gofmt 净、import 218/219、触包 282 过、race posreview+cmd/core 绿、**全套件 67 包零 FAIL**）；G2=钱径零 LLM（数字面纯 Go，LLM 只到叙事+建议枚举）、零新 env（ResolveClient=kbreport 口径复用）、≤300、迁移零新增（baseline 死表复活——合规，表在 schema 在只是零消费）、import 边规范登记；G3=posChange 订阅=8 个 ClosePosition 径结构性全覆盖（kick 风暴空扫成本<新定时器）、判决书 PK 幂等 upsert、缺口查询自收敛（已评仓自动出集——覆盖率 100% 结构性）、`exit_kind` 直读不反推+prompt 钉「权威枚举禁重分类」、parseVerdict 白名单校验违式→llm_failed+warn alert fail-visible、AI 未配=机器 note 纯数字面如实标「无 AI」、`review.Run` 聚合径保留互补（≠判决书）、followup_action 不落 fin_strategy（判据表独占淘汰权）。**观察项（不阻塞）**：①Beat 只在有产拍——idle 期名册 beat_ts 显旧属语义如实（最后工作时刻），与 posMonC 用 GoLoopPlain 不编册的分叉是设计选择（复盘员=一仓一书首类 daemon 该在编）；②expected_cost/risk 无机读源恒 NULL=票面语义（报审已述，补源后自动填）；③单仓 LLM 败只伤该行 note——Upsert 失败行下拍重试可见。G4 实拍：迁移零新增栈活 08:04；boot 补扫 **12/12 落行 6.5min**（08:04:50→08:11:24）<10min 达标；`fin_llm_call` reviewer 角色行落；0 llm_failed、12 followup_action（quarantine 类建议为主——费蚀归因叙事引用 exit_kind=legged_unwind 正确）；**fail-closed 活实证**：pos#499/500 settlement pnl_quality='unknown'→actual_net_usd NULL+note【质量痕】标「实绩质量 unknown→NULL」；名册 running=t beat 08:04:49。遗留②照票面（NULL 禁外推）；自然平仓 <10min 实证=事件径结构性保证（kick→Sweep 同拍），候下一自然 close 复拍。 | 终审席·审计通过 |
| D-268 | 2026-10-13 | **M4.6 立票+放前门核验：通过，直派实施席**。放前实证五件：①`fin_condition_gap` 态机+kinds 已在，`ResolveConditionGap` **零调用面**=业主回执口缺如（SG 实拍 #6 mt5/#11 okx:futures 两条 requested 凭据挂 2026-10-11 两日——§4.3 指标④最老请求 age 实证源）；②`capital` kind 已全程同态机（EvalCond min_usd 判+stableCcy 桶+7 模板挂载，零行=NAV≥阈值非断路）——「capital:* 走同一态机」条款落成端到端实证非新码；③投资人页 v0 纯只读零写面——回执=首个写 RPC（界窄：id+二态）；④reconcile 自收口+reopen 安全网在——回执误按下拍自动纠；⑤`ResolveConditionGap` 无 note 参但零调用面改签名安全。核验：前端零信任（按钮只发 id+态）、回执唯一写面、零 LLM、零新 env、proto buf regen。 | 终审席·放前门+直派 |
| D-269 | 2026-10-13 | **M4.6 终审席审计：通过，`628eed32` 部署 SG**。G1=diff 通读（request_box.proto 37/服务件 56/store +38/前端 +51/锚测 88）+机检（build/vet/gofmt 净、import 218/219、触包 TestM46+TestD186 绿、npm build 绿、全套件 66/67——唯一 FAIL=D587 bad-fd **第五次同型 infra flake** 隔离复跑 3 passed）；G2=前端零信任（按钮只发 id+二态、回执后重拉箱集无本地态机）、回执唯一写面、零 LLM、零新 env、≤300、proto buf regen；G3=`applied` 返回幂等双击不误报、`UNSPECIFIED→invalid_argument` 枚举闸、ListRequestedGaps 最老在前=§4.3 指标④同源口径、`capital` 零新码（同态机端到端锚实证=票面条款兑现）、resolve_note 落「投资人页回执」留痕。**遗留确认**：②waived 无 reopen=终态（票面语义——放弃即机会线终局，重估走新缺口行）；reopen 安全网照旧（回执误按下拍 reconcile 自纠）。G4 实拍：部署 08:40 栈活；`ListRequestBox` 实况 **#6/#11 两行 age=188715s（≈2.19d）**——投资人页请求箱段数据面通；`ResolveRequest` 接线证=不存在 id→applied=false（幂等径活）+state 缺→invalid_argument（枚举闸活）；两行保持 requested 未被误触（写面接线验毕不代业主翻态——真实翻态=业主点击时刻）；镜像 web dist 同构内置（npm build+Dockerfile web stage 双证）。 | 终审席·审计通过 |
| D-270 | 2026-10-13 | **D-255 终审席审计：通过，`7b79e221` 部署 SG**。G1=diff 通读（迁移 1011/venue_reject.go 61/pit.go 30/cex_signature.go 40/tryleg +52−6/dci_order +22/锚测 135）+机检（build/vet/gofmt 净、import 219/220、触包 dciexec+errclass+pgstore 全绿、race 绿、**全套件 67 包零 FAIL**）；G2=钱径零 LLM、零新 env、≤300、迁移 1011 登记、import 边规范；G3=签名规范化正确分层（code 优先取码、msg 归一数字→#+截 60——同坑聚类不撞）、LearnVenueReject PK upsert **委员会手改不被覆写**（hits/last_seen 自增而 action/params/expires 保留）、申购前查坑位正确（CreateDCIOrder 后 post 前——rejected 行留痕+零打面+一次性 critical dedup 24h）、读败 fail-closed 拒单、reqID 金额出键修幂等根因（#6/#7 实拍 `|358.00000000|` 浮点尾在键实证）、**幂等语义升级**——OpenIntent 检查在途/已成（pending\|filled\|settled）挡重发、failed\|rejected 放行新尝试（wheel 径 propID=nil 亦覆盖、LegCount 兜底历史无前缀行）。**观察项（不阻塞）**：①传输层败（dial/timeout）同落坑 suppress 24h——超「reject」字面但 fail-visible+时界+委员会可清，防锤瘫 venue 语义可辩护；②fix 行对所有该 op 调用无差别应用 params（deposit_decimals 整数化=venue 强制项下无害）；③reqPrefix LIKE 含 venue 产品串——现名规范无通配符注入面，留档。G4 实拍：迁移 1011 applied 09:31；`fin_venue_reject` 表在产（0 行=部署后零自然败行——激活待首败）；DCI 扫描链活（okx rows 434/389）；旧键实证 `dci|PUT|...|358.00000000|` 浮点尾在键。遗留②「败后有行+二次零打面」=机制 armed 状态已达，首自然败行落坑后 hits≥2 即闭环（测试库锚已证机械面）。 | 终审席·审计通过 |
| D-271 | 2026-10-13 | **DCI 轨 4 件终审席审计：通过，`d0dc8b69` 部署 SG**。逐件核裁决：#19 floorTo 已修（生产 #7/#8 settled 实证——venue 成交证据非推断）；#22 采集面 order_ref/min_amount 全落已修（NULL 行绝迹时点=修复时点）；#24 **非病灶**——venue CALL 在售 strike 域 [83750,85250] vs 模板界 ≥spot×1.10≈92122=市况无档，strike 界参数回委员会（遗留①裁定权属正确）；#25 主修 type=FAST 撤除已落 c2e7ac0e+本票残留二修四件。改件核验：①签名 `code:N|归一msg`——-9000「0 decimals」vs「refresh」同码分坑（修纯码签名混坑面）；②refresh 瞬态不落坑——一次重提后的瞬态不再误封 24h（D-270 观察项①同类面收敛）；③earn_redeem 扩坑+**读败 fail-open**（补缺径不堵——与申购 fail-closed 差异裁量正确）；④尘埃赎额 %.8f 格式化归零跳行封 -1102「amount empty」重放坑。机检：build/vet/gofmt 净、触包+race 绿、**全套件 67 包零 FAIL**（二连全绿）。G4 实拍：部署 10:19 栈活；坑表 0 行（部署后零自然败行=armed 正确）；旧键浮点尾实证在册；遗留②③如实（earn_redeem 委员会可手写 fix/critical；抑赎窗内 holding gap 如实报）。**裁决结构评注**：四件两件「已修待证」由生产行证实、一件「非病灶」有 venue 域实拍、一件真修——取证驱动裁与非盲修，合规。**委员会待裁**：#24 CALL 腿 strike 界降至 venue 在售域（≈spot×1.02）=参数裁定非码面——模板 `fin_opp_template_cond` constraint 行编辑即可，系统不越权。 | 终审席·审计通过 |
| D-272 | 2026-10-13 | **D-249①② 终审席审计：通过，`c9ddaf79` 部署 SG**。G1=diff 通读（迁移 1012/hearing.go 120/wire 35/settleadapter+10/settlement_deps+7/settlement_elim+4/engine+6/close+4/run.go+19/锚测+102）+机检（build/vet/gofmt 净、import 221/222、触包 700 绿、race 隔离 56 绿、全套件 66 包——FAIL 全归已知型：autotrade/dashboard 系我并发 race 互踩共享测试库 deadlock/dup-key、dashboard 600s 包级超时隔离 784 过、D587 第六次同型 infra flake）；G2=听证观察面（锚 TestD249_HearingObserveOnly 实证 LLM 零翻裁定权、BeforeDisable 钉 hear→disable 序）、钱径零 LLM（输入=确定性统计面快照+判据理由）、零新 env（kbreport ResolveClient 复用）、≤300、迁移 1012 登记嵌入、§4.3 注册表+§5 对象表补录；G3=clampRunes Go 侧 400 字硬闸（LLM 不守约束由 Go 收口）、ctx 200s 封顶+每路重试一次、status 三态 CHECK（debated/skipped_no_ai/failed）、落库败只 log 不阻塞淘汰主径、⑧段置 Generate 直查修 gather BJ 零点 -1d 错 report_date 口径、ListDeptReports 复用读面零新表、缺日空段注明。**观察项（不阻塞）**：①client 解析成功即 `status=debated`——双侧 side 皆败空文本也标 debated，消费面按 status 过滤需注意（状态语义略宽）；②听证同步挂淘汰径 200s 封顶=判死后延迟禁用有界，票面「击杀前」语义照办；③⑧段读败 return err 整报失败——与 gather 各查询同款 fail-visible（LOG「读败当班跳过」读作本次生成跳过）。G4 实拍：迁移 1012 applied 11:11；`fin_strategy_hearing` 表在产 0 行=armed（无自然淘汰事件——听证钩已挂两径，首事件自动落行）；栈 10min 零 panic/fatal；`fin_llm_call` reviewer×12+symdig×12（elim_hearing 角色接线证=wire 已挂 LLMLogger，首听证自动入账）；`fin_dept_reports` 存 12 旧式 dept 行（10-12 v2 结构遗产）——⑧段 Go 侧 dept 过滤不误引，今晚 23:05 UTC 首 company 行落「昨日无 company 行」注明=正确径，真对照段 10-14 起、两日验收 10-15 可核（遗留①照办）。 | 终审席·审计通过 |
| D-273 | 2026-10-13 | **NAV 幽灵池过滤终审席审计：通过，`ea73c275` 部署 SG**。G1=diff 通读（explorer.go −70→explorer_idle.go +110 文件拆分保行数闸/Ghost 字段/nav.go+5/gather+9/budget_test+5/锚测 79）+机检（build/vet/gofmt 净、import 221/222、触包 398 绿、race 280 绿）；G2=零新 env/零迁移（写侧不动=快照保留历史真值）/≤300（拆分正确）/前端零信任无涉；G3=过滤界正确——Ghost 仅标 `venue_type∈{mt4,mt5}` 行（CEX venue=场所名不入 broker_accounts 不受影响，锚实证）、stopped 户在册照计、Ghost 不打 Idle/BelowFloor（防幻影告警——观察面不收告警噪）、`SkippedGhost` 独立计数留痕（未混 SkippedNonUSD）、gather ①段幽灵行打标+剔除计数行、budget_test 补 broker 种子=在册闸下既有 fixture 适配正确。**G4 实拍**：部署 12:12；`[budget] 日切 nav=5248`；`fin_nav_daily` 行 upsert nav_usd=**5247.63**（5420.58−172.95 精确）、venues jsonb `62311223` 带 `"Ghost":true` 留痕可审计；预算行 XASSET_CARRY=4000 不动；栈零 panic。遗留照办：LatestNavDaily/IR 读面同源（ venues 面可查 Ghost 标+SkippedGhost 入 NavReport 字段）。 | 终审席·审计通过 |
| D-274 | 2026-10-13 | **MT4 退役终审席审计：通过，`df74feb0`+审计修正 `a30adcd6` 部署 SG**。G1=diff 通读（16 件 +86/−82 手术面）+机检（build/vet/gofmt 净、import 221/222、触包 2000 绿——7 FAIL 全为共享测试库并发竞争抖动，隔离复跑全绿实证、race 415 绿）；G2=写边界五面收口（InsertBrokerAccount/splitVenueKey/comboVenueTypes/automap/graduate_resolve 全拒 platform!=1 或 :mt4）、CHECK 约束与历史读面保留（③档不动 baseline）、零新 env、前端 web/src 零 mt4 选项（gen pb 枚举注释属③档保留）；G3=`ClassifyMT4` 码表整撤 mt4 键→Abort 兜底正确（CEX 原就 Abort 同径）、后缀候选撤 mt4、分类清单三档逐文件标注落 LOG。**审计修正 1 件**：`df74feb0` 把归档写进仓库根 `LOG.md`（应为 docs/handoff/LOG.md）——内容对位置错，终审席归位删散件（`a30adcd6`）。遗留③照办：`brokerVenueType` platform≠1 渲染「mt4」=存量行忠实读出（其数据源 platform 只产 0/1，未知值不会走到）——若要「未知不猜」语义一行可改，不阻塞。**G4 实拍**：部署 12:36 栈活迁移 applied；`AddBroker platform=0` RPC → `invalid_argument: MT4 已退役：platform 仅支持 1(MT5)`——写拒行为生产实证零行落库；前端 mt4 选项残留=零（grep 实证）。 | 终审席·审计通过 |
| D-275 | 2026-10-13 | **能力账本凭据归一病灶终审席审计：通过，`7e143a26` 部署 SG**。G1=diff 通读（capability.go +3−1 最小面/双锚 66 行）+机检（触包 pgstore+oppmatch+deptreport 全绿、race 25 绿、import 221/222）；G2=零迁移零新 env；G3=修法最小正确——`VenueType=vt` 真值注册（type:mt5 键通）+`cfd` 语义经 Tags 保留不丢、grep 实证 `type:cfd`/`subject:cfd` 零消费面（残余 "cfd" 全是 AssetType 域不同概念）、#11 判**非病灶**裁定正确（term_basis=现货×远月期交割腿，okx:futures subject 正确、在册仅 perp=如实缺口，注册 okx 交割线归委员会不越权硬改）。**审计修正续案**：本票 LOG 曾同样误建根目录，实施席自纠 `ded52c03`（续 a30adcd6 模式——自纠行为值得记）。**G4 实拍**：#6 `fulfilled` 12:37:49——修复随 12:36 栈起后 reconcile 一拍**自动收口**（票面验收「下拍自收口」实证）；#11 requested 保持=如实缺口；栈零 panic。**遗留**：#6 请求行终态 fulfilled；#11 等委员会 venue 裁定（okx 交割线接入 or 模板改指）。 | 终审席·审计通过 |
| D-276 | 2026-10-13 | **D-249④ 日报 SOP 契约终审席审计：通过，`9ca6b801` 部署 SG**。G1=diff 通读（contract.go 51 行九必段探针+run.go +16 接线/锚测+72）+机检（同批触包全绿、race 绿）；G2=schema 只管结构不管数字（数字红线仍在系统提示+scrub）、fail-open 落库不丢报、零新表零迁移、钱径零 LLM 涉入（契约校验=输出结构面）；G3=探针序位严格递增防乱序、prev 不推进防连环误判、补正注记并入 user 输入重生成一次、仍违正文追【契约缺段】标+log（fail-visible 三件套齐）、fakeRunner 样例升级不破旧锚。**观察项**：①补正重生成后 `res=res2` 只记第二班 tokens——行字段欠计首班（fin_llm_call 逐次入账不受影响，留痕面微欠，不阻塞）；②探针关键词命中制容忍 LLM 自拟标题（遗留②声明的取舍），漂移误检可名单微调。**G4 实拍**：部署 12:49 栈活；今晚 23:05 UTC 班 company 行全序段齐=验收项待自然拍（违约则正文见「契约缺段」标+log 可核）。**D-249 四件至此全交付**（①听证✅ ②待办闭环✅ ③D-250 裁弃 ④本票✅）。 | 终审席·审计通过 |
| D-277 | 2026-10-13 | **评估卡证据源补强终审席审计：通过，`4612b166`/`e6ff4f63` 部署 SG**。G1=diff 通读（eval.go +19−3/opp_eval.go +55/锚测+86）+机检（build/vet/gofmt 净、触包 18 绿、race 18 绿、import 同基线）；G2=零迁移零新 env、零外推不破（缺源仍 missing 不估）、econ.Evaluate 纯算核未动；G3=口径裁量三处正确——earn failure 接 `fin_venue_reject`（申赎拒绝在资金移动前=零损耗，同 DCI 口径一致可辩护）、funding fee=taker 中位×双腿按 realized 持有窗年化（venue 实测值域非模拟行，median 全集中位不偷挑单所）、failure=position_legs FAILED 账本（0 败=零损耗证据；>0=成本未计价仍 missing fail-closed，遗留②照办）。**G4 实拍**：13:10 boot scan——earn_apr_lending/cash_mgmt 两翻 `viable`（failure 证出 `fin_venue_reject earn_redeem n败`）；funding_carry missing 收敛 [fee,slippage]——fee 缺属正确 fail-closed（生产零条 CLOSED PERP_FUNDING→持有窗无账本）；**挖到预存潜伏 bug（非本票引入）**：`eval.go` slippage 源 `LatestSpreadBps("BTCUSDT")` 键型错配——生产 `fin_cex_spread.symbol` 存基础币 `BTC` 形（8526 行鲜数据永不着），属旧病灶另案修。**观察项**：PERP_FUNDING 零历史→fee 永缺鸡生蛋面，委员会若要兜底口径（模板声明典型持有窗）属参数域另裁；cross_venue_spot 五项全缺=detector 未建确认另案。 | 终审席·审计通过 |
| D-278 | 2026-10-13 | **XASSET_CARRY 开仓阻塞取证终审席核验：通过，零码纯取证 `fd4b5f9f`（LOG 归档）**。五断言全生产核实：①books 端点在线（fin_cex_depth okx XAU-USDT-SWAP 4402 行鲜）→「OKX tickers 不带 depth」假设推翻，真断点=采集宇宙(≈45 基名)⊂候选宇宙(1093)；②live 零开仓主阻=**经济学非管线**——okx XAUUSDT funding 实测=0、equiv_annual≈0.12%、XAUUSDm swap_long=−1.29bps/d（canonical XAUUSD 行置 0 为 swap-free 口径副作用属实）→detector net≈0 →评估−margin=−4.5→RatingTrap 不 publish=**闸按设计拒开正确**；③503/504=双腿即拒（5ms 双 FAILED filled=0），closed_at NULL=设计语义（FAILED 无仓无平），fail_reason 列 0201 后建故拒因不可复原——同期 507/508 型对拍=MT 腿间歇拒单已见形态；④白名单唯一 live 行 `XAUUSD|XAUUSDT 3001157:mt5×okx:perp` enabled+非模拟核实。**修复面 R-A~R-D 裁定**：R-A（depth 覆盖对齐候选宇宙）=体量评估票——1093 基名×3 所 books REST 轮询需 D-004 例外留痕+限流测算，值派；R-B 无需修（新失败自带 fail_reason）；R-C=**委员会策略域**（等 funding 转正 or 换 venue/标的——XAU×okx 结构性无肉）；R-D sizing 疑点低优先另案。**附带观察**：XAGUSDT-SILVER monitor equiv_annual≈9.97% 正息差浮面（whitelist 未 enable live——若委员会想试银线属参数启用裁定）。 | 终审席·取证核验通过 |
| D-279 | 2026-10-13 | **fin_cex_spread 键型错配修复终审席核验：通过，`fb2a9232` 部署 SG**。一行级修复（`LatestSpreadBps("BTCUSDT")`→`"BTC"`）+写侧键义注释留档；票面 grep 要求闭合——`LatestSpreadSpotRef` 本就用 `"BTC"` 对键，全仓无第二处同病。机检：触包 19 绿。**G4 实拍**：13:48 boot scan funding_carry missing 精确收敛 `["fee"]`——slippage 出证（fin_cex_spread:BTC 8526 行源接通），fee 缺=设计内 fail-closed（PERP_FUNDING 零历史）。 | 终审席·核验通过 |
| D-280 | 2026-10-13 | **cex-depth 采集宇宙对齐裁定：采纳方案 A（分所精集）**。测算核验（SG 实拍）：候选实集 98 基名 vs 现采≈45、58 缺口与 depth_missing≈50/拍 吻合；A 案 206 calls/5min≈0.7/s 三所限内（binance weight2/2400min、okx books sz5 公开档、gate 宽松）、串行 41s<5min 闭。裁 A 而非 B：分所精集零空打（B 并集跨所 ~33% 空打在所无符号=耗配额买零），funding 见证基名=候选腿真实集合天然自对齐。C top-N 封顶记档为扩容闸门备件（98 现状无须）。D-004 例外留档成立：books REST 5min 采样=纸面质量闸研究面（D-420 先例），非执行/判定钱径；WS books 频道存在但快照采样语义不改数据性质。**GitHub SSH 抖动定性**：mihomo fake-IP 拦 ssh:22/443 上游节点抖（间歇可通，cef3beb8 实证）；彻底修=业主域（gh token 重登切 HTTPS / 订阅节点固定），临时=push 重试环兜底不阻塞部署（SG 走 rsync）。 | 决策席·裁定 |
| D-281 | 2026-10-13 | **cex-depth 宇宙对齐方案 A 终审席审计：通过，`a91a3b0b` 部署 SG**。G1=diff 通读（funding_witnessed +31 新读件分所签名/cex_depth 拆分单所采样旧签名委托保留/wire_depth bases(venue) 分所化/旧 ListCfdUniverseDepthBases 零消费撤净/锚测改分所隔离锚）+机检（触包 337 绿、race 58 绿、gofmt/vet 净）；G2=零迁移零新 env、节奏 5min/保留 90d 不动、静态白名单 5 对所全量保采（研究面锚定不退化）、读败退化静态集 fail-closed；G3=`fundingWitnessWindow=6h` 裁内取值合理（funding WS 断流>6h 缩采面=fail-closed 向同旧语义，遗留①照办）；C 案 top-N 未实装符合裁定（98 现状无须）。**G4 实拍**：witnessed 分所计数 binance 93/gate 76/okx 37=测算精确命中；`depth_missing` ~50/拍→**1-3**（残余=okx SPX500 empty books 类 venue 未上市如实缺口）——票面验收达标；`depth_low` 22→59-65=预期位移（深度数据从无到有后簿薄真相浮出=质量闸真起效）；opened=0 持续=经济学问题归 R-C 委员会域非本票面。 | 终审席·审计通过 |
| D-282 | 2026-10-13 | **dci_buy_low 晋级 live（委员会迁移，终裁席裁定执行）**。业主令「你是第1负责人，由你裁定」授权下晋级。**裁量依据**（策略评估 skill 口径）：①统计面诚实——settled N=2（358+541 USDT 盈利）属**证据不足级**，晋级论据=机制健全非胜率；②风险形状有界——1 日 DCI put-sell、名义~400-500 USDT≈8% NAV 远低于 CEX≤35% 硬线、隔日自结、最劣=strike 接货 MTM 亏；③机制面已验——执行器真金 2 单 settled、CheckDCI 实时闸全保留（提案快照非钉价）、坑表 armed；④结构事实——`fin_strategy_criteria` 对 dci_buy_low 零行，确定性 graduated 径未接线，委员会显式迁移=设计内唯一通途。**执行径**：fin_strategy+strategy_stats+alert 三写复刻 SetStrategyState 语义（decided_by=committee）→重启 boot scan→**AutoExecLive 首次真金点火实证**：#222 PUT 腿重取报价（2700070 旧档→2700121）→venue 产品已下架→诚实拒单 `fin_dci_order` #9 failed 留痕、提案不吞仍 proposed 待下拍。**机制端到端=正确**（放行→重报→拒单→留痕全链通）；残余观察：下架品若被 quoter 反复选中将逐拍败（选档坑议题，一单样本先记）。**earn 两提案 viable 但不可执行**——ExecTemplates 仅注册 DCI 系，earn 执行接线属另案票。 | 决策席·裁定执行 |
| D-283 | 2026-10-13 | **E0 纪元归档裁定（业主令「原数据全量归档新纪元起算」）：现在做，形态=封存非删除**。裁量：①时点=跨正式营运线之日（D-282 dci live 首燃）为天然纪元界——等「稳定盈利」反而把修复期脏样本（费蚀 12 仓 −64.76/v2 部报/MT4 期行）带入新纪元统计；②形态=`arch_e0` schema 封存+pg_dump 两落点（SG+R2），账本纪律不破可逆可审计；③代价声明——eval 失败证据变薄（fin_dci_order 6败史归档后 failure 项出 `0败`）、realized 持仓窗清空、fin_nav_daily 仅留最新锚行（30d 回撤基线不断）；④分档三档定死票面（全表归 27/终态行归 7/留活 59 含 facts 行情窗+坑表+能力账本+fin_strategy live 态+请求箱在途行）。执行径：实施席交 `archive_epoch0.sql` 脚本→终审席审→SG 落库。 | 决策席·裁定 |

## D-284：E0 纪元归档执行闭合（业主令 10-13 落地）

**裁定**：`scripts/archive_epoch0.sql` 终审通过（0cc182c3），终审席 SG 亲执。
**铁序实证**：pg_dump -Fc 207,344,136B 双副本——SG `/opt/nox-backups/` + 本机 `/opt/nox-epoch0/`，sha256 `6ca4b0c0` 两端一致；R2 径未配（r2: remote 桶凭证失效，backup-r2.env 空值=日常备份本就 local-only 径）→第二副本改本机，铁序语义不破。
**执行**：pre-flight FK 闸零出边→单事务全做——CTAS 33 表+TRUNCATE 26+DELETE 谓词 4+nav 锚回插。对拍全绿：全表集 live=0；在途留活 fin_proposal 8/explorer 40/paper active 36/trx pending 427；fin_nav_daily arch=1 live=1（锚在）；fin_dci_order 整表留活（FK 一致性裁，记偏离）。
**验收**：栈重启零 panic；fin_strategy live 双行在（dci_buy_low/XASSET_CARRY）；gap#11 requested；facts 6,666,903 行零损；proposed 提案 5 在（#222 autoexec 资格保）。
**E0 起算**：新纪元统计口径自 10-13 15:00 UTC 起；旧账在 arch_e0 schema 可查可逆。

## D-285：币种口径裁定——USDT 即 USDT、USD 即 USD，零换算（业主令 10-13）

**裁定**：财务展示一律按账户原生币种如实呈现，不做 USDT→USD 折算；`SumNav` 的 `SkippedNonUSD` 口径从「缺口」转正为**正确行为**保留。
**理由**：业主令明确——各币种真实数据各自展示；折算引入的汇率假设比币种分离的代价大。
**衍生**：财务页（下票）按 currency 分组展示 equity/idle/deployed；NAV 维持 USD-only 语义不改（USDT 池不上 NAV 合计属裁定内）。

## D-286：金钱不眠——业主令 10-13 增列总纲锚

**裁定**：AGENTS.md §1 增「金钱不眠」条款：闲置资金=待部署缺口，系统须为每一分本金持续枚举承接位（现金管理/赚币/低风险计息档），仓位空隙本金不停息。
**边界**：不造劣构交易——承接位必过评估卡闸，风控硬线高于利用率；找不到合格承接=如实报闲，财务页 idle 列=不眠度计量表。
**衍生票候**：①earn 提案执行接线（ExecTemplates 现仅 DCI 系，cash_mgmt/earn_apr_lending viable 但不可执行——不眠度直接阻塞面）；②财务页 idle 列承接计量。

## D-287：文档轮转规矩（业主令方向——token 成本+防漂移）

**裁定**：① STATE.md 轮转——完票行 >7 天沉 `docs/handoff/state-archive/`（按纪元分段），工作面只留待领/在途/近 7 日完；② decisions.md 纪元分卷——条目 >200 行或纪元归档时切 `decisions/e<N>.md`，索引每行带卷指针；③ decisions 单行体型闸——编号+日期+结论+一句理由，长取证一律 LOG.md（原规矩回收）。
**理由**：账本面膨胀=token 成本+漂移面；分卷/轮转保锚不动内容，索引是唯一查询面。

D-288｜2026-10-13｜cash_mgmt+earn_apr_lending 升 live：earn 执行接线验收后由 committee 晋级（decided_by=committee:D-286/D-288）；理由=金钱不眠义务下承接位必须可执行，闸序已实证 fail-closed（首拍即 47.8%>35% 如实拒单）。

## D-288 撤 CEX 总占比敞口闸（业主令 2026-10-14）

**业主令原文**：「钱应该活起来，那里有机会就应该流向那里去」「不应该有这个门槛……钱自然要流动起来才能创造更高的利润」。

**改**：earn/cash_mgmt 执行闸的 CEX 总权益占比上限（35%）**撤除**——申购不再因「CEX 占比超限」拒单。

**保留（系统自盯，非闸）**：单 venue 或 CEX 总集中度超阈（单 venue≥50% 或 CEX 总≥70%）→ info 级 alert 如实标浓度——只透风险不拦流向。

**决策席异议留痕**：推荐过「单 venue ≤40%」硬顶，业主裁撤；30 天 −3% 全停、事故仓停新开、CFD≥25%、90天≤3oz 其余硬线不动。

## D-509（2026-10-14 决策席）：DCI PUT 行权界 ATM→5% OTM

**实证**：`dci_put_otm_bound=1.00` 允许贴价卖 PUT（SG 在册 strike 83000-83500 vs 现货 83427≈ATM）；贴价卖看跌=约 50% 概率接货的裸方向赌注，盈亏结构非 carry 定位。

**改**：`fin_capability_constraint.dci_put_otm_bound` max 1.00→**0.95**（SG 已改）。接货从「抛硬币」压回尾部事件；权利金 APR 少收换「接货时先跌 5%」的缓冲。

**不动**：`dci_call_otm_bound=1.10` 对称界不变；qty 小额/tenor 上限/settle 记账径照旧。接货后 BTC 库存处置径（自动卖回 vs 转 sell_high 接住）另行立票——机制设计非参数。

**域**：策略执行参数属决策席域（D-184 全权），业主域只涉方向锚本身。

## D-510 S1 首批假设裁决（4 拒 3 留）2026-10-14

- **裁**：rejected=`binance|PERPETUAL|BTC`（币报价族）、`binance|PERPETUAL|USD1`、`binance|PERPETUAL|U`（边缘稳定币报价，流动性薄）、`okx|FUTURES|USD`（币本位交割=设计稿明定另一族）。留 proposed=`binance|PERPETUAL|USDC`（真货架可顺手扩）、`binance|TRADIFI_PERPETUAL|USD1`（股指永续=新资产族最值得研究）、`okx|FUTURES|USD_UM_XPERP`（XPERP 语义待查）。
- **语义**：rejected 走弃件记忆抑重推——同键不重返；留件等 S2 通用族落地再评，不自动批准。

## D-511 XAUUSDT 系统外单收编口径 2026-10-14（业主授权决策席拍）
- **裁**：中间态——外单计入敞口面板（行标 external）但生命周期永不接管（不开不平不追踪）。推翻「维持现状」：$8 名义虽小，但敞口盲是原则错——外单是真实账户 delta。
- **边界**：仅展示面并表，进敞口/浓度透视；交易径零接管。

## D-512 CMC 手工仓位只读纳管 2026-10-14（业主授权）
- **裁**：收编为只读展示行——手工录入径→快照域落行标 `manual`，进业主可见资金全景；不进敞口计算不风控不追踪（外来仓位同 XAUUSDT 判例）。
- **禁线**：不编值不估算，缺数据=缺行如实；仓位动不了。

## D-513 毕业委任规则（确定性自动放行）2026-10-14（业主授权）
- **裁**：批。pending≥3天∧paper龄≥14天∧accrued>0∧flag_warn空∧单策略额度≤$500∧品种限既有 live 模板域∧日批≤1∧逐笔 warn 告警∧app_config 一键可撤。
- **理由**：缺席期策略永不毕业=进化链断；确定性规则可审计 vs LLM 副席非确定性——前者优（同 D-184 精神）。额度 $500 保守起。

## D-514 DCI 去留裁定：留+机会成本闸 2026-10-14

- **裁**：dci_buy_low 保 live 不淘汰。账本实证：N=2 已结收权利金 $2.80（证据不足标注，<30 样本）；8 单 failed 全系管道瑕疵（参数格式/报价过期/产品下架竞态）非策略亏。风险面 D-509 已收 5% OTM——接货变真尾部事件。
- **新闸**：premium_ann < max(3×earn_apr, 8%) 不开——溢价不足以补偿尾部风险的日子钱回 earn 活期；DCI 由常开策略变机会策略。
- **复审锚**：N≥30 或首次接货时复评；接货处置径立票 pending（机制缺口 LOG 已记）。

## D-515 income_events 纪元门裁定：不设门（venue 权威全收）2026-10-14

- **裁**：income 采集径不加 E0 纪元过滤——venue 权威流水全收全记（ts=venue 事实时刻，created_at=落簿时刻）；E0 后「从0计账」口径由展示/归因层实现（created_at 或 position_id 归属过滤），非采集层。
- **理由**：①账本不记事≠没发生——venue lookback 窗有限，挡掉的流水永久丢失；②门会吞 venue 日后的旧 ts 补记（追溯调整/返利），悄丢事实；③position_id 空的无归属流水是天然「账实不符」探测器（外单/采集缺口信号），掐掉=丢报警器。
- **配套（记 LOG 不立项）**：收入面板按 position_id 分「策略归因/venue 无归属」两栏——回流旧账落无归属栏。

## D-516 BTC 共享持有归属口径 2026-10-14（业主供证：仅占 1/3）
- **裁**：binance earn 0.0568 BTC 按 1/3 归属本基金资本（~0.019 BTC≈$1.57k），2/3 为代管外来资产。持仓不动（活期形态无妨）；NAV/敞口分母只计 1/3 归属份。
- **实现径**：待立票——资金地图/分母读径对 BTC earn 行挂归属系数 0.3334（配置驱动可改，禁硬编码）；外来 2/3 标 custodial 不进任何风控判据。

## D-517 DCI 接货处置：自动现货卖回 2026-10-14
- **裁**：PUT 行权落 BTC→自动现货卖回 USDT（市价单，即时结清尾部代价）。不接「转 sell_high 等回调」——事故驱动持仓违「只为设计过的生命周期持仓」原则；wheel 轮动若做须独立设计票。
- **边界**：卖回径沿用既有现货执行面+venue 权限实查（无权则告警不臆造）；行权 BTC 进账侦测→卖回→收益差如实落 income_events（亏损也如实记）。

## D-518 cfd_swap 首批假设裁决 2026-10-14（38 条 proposed→7 留 31 拒）
- **口径**：裸 delta 敞口的息必须显著跑赢 earn 基线（~2.4%）才配活——ann<2.5%=息不抵险拒；符号别名同值重复（XBRUSD/XTIUSD/SILVER=BRENT/WTI/XAGUSD）拒别名防重复曝险。
- **留 proposed 7 条**（15032313 户）：BRENT 5.4%/WTI 5.0%/XAGUSD short 3.7%/XAUUSD short 3.7%/AUDCHF 3.4%/USDCHF 2.8%/AUDJPY 2.6%。
- **approved=零（有意）**：裸 delta 族≠可跑策略——从「收息面」到「可开仓策略」差一层对冲/篮化结构设计（同 DCI 批评面：息是小头是方向赌注）；S2 触发器不因本批激活。留件义=保留观察+未来对冲化设计时再议。
- **结构新证**：38 条全产自 15032313（$1k）；3001157（$3.2k 大头）零正 swap 面=结构性零息账户——业主域裁决（挪资/认沉淀），挂待议。

## D-519 请求箱四件调配裁定 2026-10-14（业主授权：由我调配其配合）
- **capital|3001157**：全额划转 Exness→binance。理由：Exness 该户目录零正 swap 面=swap-free 结构性无 carry 位，CFD 策略形态在此户无承载；CEX 侧有活引擎（earn/DCI/日历基差）。请求文已改写为执行指令，业主 wire 后 fulfilled。
- **executor|okx:unified**：API 实查可行——OKX `POST /api/v5/finance/savings/purchase-redemption`（side=purchase/redempt，可选 min rate 0.01-3.65）。立实施票接 okx earn 写径，接上即闭环金钱不眠。
- **executor|okx:earn**：粉尘 $169.5——业主令「不计」，行已 waived；顺带 idleGapScan 加 <$100 小池阈值防粉尘件再入请求箱。
- **executor|gate:perp**：API 实查可行——gate `POST /api/v4/earn/uni/lends`（type=lend/redeem，min_rate 小时结算）。同票接径；gate 池纯保证金无持仓无策略，$998 进 uni-lend 灵活放息。

## D-520 决策席纪律闸 2026-10-14（业主令——决策席上移问句病灶纠）
- **病灶**：决策席反复把自裁域上行成对话问句（「要你拍」「要不要立票」「业主域裁决挪资」），违反 D-184 分工——业主只响应请求箱与真金授权，不坐决策席管道。
- **裁**：AGENTS.md §3 增决策席纪律四条——裁定行文=决定+理由+配合项禁问句上行；配合项一律落 fin_condition_gap 请求箱；能进机制不进会话；业主待判断仅三件（真金授权/请求箱回执/方向锚）。

## D-521 假设裁决第二拍+裁决队列浮面 2026-10-14
- **裁决**：17 行 proposed 全留零批准——cfd_swap 裸向 9=D-518 留观件等对冲化设计；cfd_swap_hedged 7=对冲腿假设净息 0.7~3.5%/yr 不显著跑赢 earn 基线且两腿执行是 novel 机制非 generic_yield 参数；venue novel 3=无族可入无机制桥。S2 冻结面正确续命。
- **结构缺口**：裁决队列不自浮面——proposed 积压是环境态非任务面，S2 激活依赖裁决席想起开拍=不稳依赖。浮面件另立票。

## D-522 NAV 按结算币种单源（2026-10-15 晨检裁定）
- **实证**：`SumNav`（pgstore/nav.go:36）currency='USD' 恒等过滤——CEX USDT 池 ≈7794 全跳过，`fin_nav_daily`=4247.63 仅为实盘 ~12k 的 1/3；配置器 `nav×(1−reserve)/live` 与 D-030 分母同失真（D-519 移资 CEX 后矛盾显形）。
- **裁**：NAV 不改「单数」语义，改**按结算币种分账**——`fin_nav_daily` 变 (day,currency) 行（USD/USDT 各一，零换算不跨币加总）；配置器预算派生键=策略结算币种（earn/dci_buy_low=USDT 读 nav_usdt，cfd/xasset 类=USD 读 nav_usd，结算币种=提案 invest_ccy 同口径）；投资人页/日报双行展示。
- **边界**：零换算红线不动（跨币永不加总求 NAV 总额——D-030 目标分母同受 D-285 约束，按币种分别记账评估）；USD-only 行存量不迁（历史如实）；预算缺席行仍 fail-closed。

## D-523 快照读面=账本真值，两写口对齐（2026-10-15 晨检裁定）
- **实证**：gate:perp 快照 998（9-30 化石，venue 实有 perp=0 现货已出借）/binance:perp 1000（实有 1344）/binance:funding 9-30 化石；binance spot 曾落 0 而 venue 实有 1088（WS 事件漏发窗，REST 校准只回写活账本禁写快照——D-132 行 82「WS 覆盖键不写」）；gate:earn 键无写口（venue 实有 1999.64 uni-lend 出借）。而 `ExplorerIdleFunds`/`SumNav`/`CapitalPoolSnapshots`/资金地图 idle 列全部读此表当实时面——「审计留痕」与「消费端当现值」矛盾。
- **裁**：`fin_venue_account_snapshot` 定位恢复=**账本真值面**（读端无需改），写口二补——①REST 校准/活账本回写发生值变时同步落快照行（src=rest_calib），WS 覆盖键不豁免「值变即写」；②WS 重连成功（onReconnect 既有钩）触发一次 venue 余额实读→与末行比对值变落行（断窗漏事件自愈）。③gate:earn 键接入 rest_poll 写口同 binance:earn 先例。零新定时器——两径皆事件驱动（校准事件发生/重连事件发生）。

## D-524 earn 收益增量归属入账（2026-10-15 晨检裁定）
- **实证**：okx savings `earnings` 累计 70.89、`loanAmt` 复投增（#26 下单 1938.00→现 2117.77 含业主另入）；gate `lend_records` type=reinvest ~0.0054 USDT/h 落 `lent_amount`（venue 实拍 1999.5978→1999.6412）；binance earn 同构。`income_events` 全期 earn 收益=0 行——D-030 ≥8% 缺账本证据面。
- **裁**：earn 池（venue_type=earn / dci_order 理财形 filled 行 venue_key 面）收益归属=**增量法**——池行 equity 新写时 Δ(equity)−已归属流（本系统申购/赎回单、已知划转）=收益增量 → income_events 行（income_type=earn_yield，amount_usd=Δ 面值——零换算面各币种同视）；venue 侧逐笔可查的（gate lend_records reinvest、binance earn history 如可取）优先按 venue 回执归，取不到走池增量。负 Δ 无对应赎单=如实负收益行。
- **边界**：增量归属挂快照写径（D-523 写口同源同拍），零新定时器零轮询；BTC 归属份（D-516 share=0.3334）收益同乘系数；收益归属不做税/费拆（venue 净值即实收）。

## D-525 假设裁决第三拍+两取证票（2026-10-15 晨检拍）
- **裁决**：18 条 proposed——**拒 1**（#2 binance PERPETUAL|USDC：报价币种变体非机会假设，属宇宙枚举覆盖面轴，弃件记忆）；**留 17**——cfd_swap_hedged 8 条与 cfd_swap 裸向 6 条、TRADIFI_PERPETUAL|USD1（样本薄观察）、okx FUTURES|USD_UM_XPERP（待取证）维持 proposed。
- **关键判据**：cfd_swap_hedged#505（short XAUUSD@15032313 × long PAXGUSDT@binance perp）净 carry +6.4%/yr=唯一显著跑赢 earn 基线；D-521「novel 机制」拒因对该形态不必然成立——short CFD+long CEX perp 恰是 XASSET_CARRY 执行轨同构形态（PAXGUSDT 在 binance perp 目录）。**批准仍不落地**——表达性未实证前批准=造死端；立取证票定「xasset 轨能否表达此向」，可则全族转提案模板票。
- **USD_UM 面**：okx 交割基差晨拍 ETH-USD_UM-261030 +89%/SOL +82%/BTC +63% ann（15 天到期）=全库最强原信号；USDⓈ 结算币种/抵押面/ctVal/现货腿可行性未知，立取证票——可行→日历基差 P0 轨扩 okx venue 施工票，不可→#7 拒件记忆。
- **gate 面**：uni-lend DCI 报价 8.22%×1d 入采集但无提案模板（venue 枚举只 binance/okx）——记缺口不立票（量小）；gate funding 极端负费率（NIL −365%/PONS −267%/SOXS −227%）=微盘币流动性面如实记，不产假设（坑>利）。

## D-526 DCI 选档尾险定价（业主疑问落地——DCI=卖尾险须对尾定价非对息比价）
- **病灶**：`dci_put_otm_bound=0.95` 平界+选档取 APY 最优→系统结构性贴界选最浅 strike（实证 #445 BTC 77500=5.3% OTM×16d≈0.3σ、#30 ETH 2280=6% OTM×6d）；溢价闸基准=3×earn——对无风险收益比价，对所卖尾险零定价。浅虚值 PUT 期望为负（16d premium≈0.65% vs 行权条件损失 4-6%×概率 35-45%）。
- **裁**：①OTM 界改 σ 驱动——bound=1−k×σ_daily×√tenor（σ_daily 读标的实现波动率 facts/ticks 实算，k 与 tenor 上限落约束表 dci_put_otm_k/dci_tenor_max_days，禁字面量）；②选档目标改「Δ(premium_ann−tail_cost_ann) 最优」——tail_cost 估算=行权概率×条件损失×365/tenor（实现波动率定价法，同 σ 源）；③在仓 DCI 名义合计帽入约束表 `dci_open_notional_cap_usdt`（默认 NAV_USDT 的 15%——浅虚值叠加崩盘的复合风险帽）；④既有在仓不回追（#30 $10 探针 10-21 自然到期；binance #11 2d 短档自然到期）。**结果预期**：σ-safe 距离下多数提案将如实被溢价闸/尾险闸拒——那正是「安全版不值钱」的如实表达，不为追单放闸。
- **定位**：DCI 产品本身非 venue 坑（OKX/binance 标准结算），坑在选档参数化——本裁只动选档与闸，不动产品接入面。

## D-527 评估席完整判据（业主问「评估席如何完整」的裁定口径）
- **完整定义**（四件全立才算完整）：①一提案一卡强制（已有：run.go 每拍复算）；②**每激活模板族带五项证源装配**——新族激活票必须随附 term 证源映射件（eval.go 硬编码 switch 是现状可接受的 fail-closed 形态，激活即补，不预先全写）；③**评估卡闸覆盖全执行径**——现状只 dciexec/earn 径吃卡，xasset engine 老轨不读卡（历史轨豁免观察，新轨必吃）；④**actual_apr 全执行径回写**——现状仅 dciexec settle 两点，earn/carry/xasset/cash_mgmt 退出点都要回写，G10→G2 校准环才有完整右腿。
- **第五项裁定**：五项拆解对线性 payoff 族完备；**非线性/负偏度族（卖权/裸 delta carry）须第六项 `tail_apr`**（缺证→unevaluable 同例，线性族 mechanism-豁免恒 0）——D-526 σ 源落地后随件扩列，不提前建。
- **指标面**：§4.3 ⑤预实偏差=actual_apr 回写覆盖后才入日报自算（现在 1/25 回写率，算了也是噪声）。

## D-528 非稳定币现货计价入 NAV（推翻「非 USDT 不读」旧口径）
- **背景**：现货 reconcile 按旧令只计 USDT 标（cex_account_binance.go 过滤非 USDT/LD*），当时 BTC 持仓是尘埃零头合理。业主 10-15 对账实拍：binance 现货 BTC 0.0127715（~$1034≈本金 8%）+io 表每 ~2 天一笔 0.005-0.007 BTC 外部充值持续进账——重要性阈已过。
- **裁定**：非稳定币现货持仓按公允价计 NAV，currency 独立桶（BTC/其他），不进 USDT 桶（D-285 分币口径扩）。LD* 前缀仍不计 free（earn 持仓回显，另票修）；dust<1 USD 等价可滤行但须计合计或记豁免。
- **域界**：账本计价口径=决策席自裁域（非业主三件），推翻旧令由决策席背账；业主 App 对账行数是唯一终判据。

## D-529 现货分币快照口径落地（LD* 处置选型 + okx/gate 同型核查结论）
- **LD* 处置选型（票裁「二选一」落=排除）**：LD* 资产从 spot 域整体排除（free/equity 皆不计），而非保留+NAV 去重——binance:earn 池行（flexible+locked 端点）已同值独立记账，spot 保留任何 LD* 形态都会与 earn 行双计且跨键去重脆弱。真 LD* 代币白名单：LDO（Lido DAO——其 earn 镜像应为 LDLDO）；venue 新增真 LD* 代币须同行补白名单。
- **分币行载体**：快照 DISTINCT ON (venue,venue_type,currency) 原生分币行，原生量零换算（D-285）；WS 事件帧（outboundAccountPosition）同写 qty>0 非 LD 行（D-004 流主源），qty=0 不写、消失币由 REST 校准径 prev 并集补零。尘埃阈 qty×价<1 USD 滤行（价源=公开 /v3/ticker/price 全符号单读，读败 fail-closed；无 XXXUSDT 对币不滤不估价 UsdtValue=0 如实留行）；USDT=本位锚行恒留。
- **binance spot 聚合语义**：free_margin=裸 USDT free（exec 可动读源口径）；Equity=Σ 分币×USD 价（展示域估算，非账本列）。
- **okx/gate 同型核查结论**：okx unified details 全币枚举已接（eqUsd/availBal venue 已供零新调用，尘埃阈 eqUsd<1）；okx funding 全币枚举（无估值字段原生量）；okx savings 在 finance 户不回显 unified（无双计）；gate 现货无 spot 读径（G1 仅 perp）、gate lend 已独立 earn 键——无双计源。okx WS account 帧仍只写 USDT 聚合行（margin 域口径差异——REST 校准径补分币行，缓议不阻）。
- **DCI 入快照口径**：binance HOLDING 行 subscriptionAmount 按 investCoin 归位 earn 分币行；okx sfp/dcd/orders live/pending_settle 行按 notionalCcy/investCcy 归位。台账↔venue 对拍（filled 单 product_ref 缺席 venue 在仓集→warn 24h 去重）。
- **NAV**：SumNav ByCcy 放开全币种原生桶（'' 残影组仍不入）；SkippedNonUSD 语义改=非 USD/USDT 池计数（审计面，不再跳过）。

## D-530 提案↔仓位桥封口径（b 路裁定——「提案驱动仓位桥定径」票）
- **取证**：①提案轨产物=fin_dci_order 行+fin_personal_position 表象行，从不写 `positions`（dciexec bookSettlement/earn 径核实）；actual_apr 提案族回写已由 dci settle 与 earn 池增量两径覆盖，不依赖仓位桥。②positions 全量=autotrade 引擎直产（dedup_key=combo.Key），request_id 属引擎幂等键域，与 fin_dci_order.request_id（dci|/earn| 域）结构性不相交；fin_proposal.params->>'position_id' 全库零写径。③carry/xasset 的实测-预实对照已在 settle 同点位（UpsertVerificationSample/UpdateStrategyStat/checkXassetPredictVsActual）——提案回链非其真值环载体。④(a) 路成立前置=提案驱动仓位执行轨（归属语义一提案一仓/多仓合并+schema+引擎改造），属 M 阶段方案级另票，现无轨可接。
- **裁定**：carry/xasset 及一切 autotrade 直产仓永不经提案轨（引擎直产=终态语义）——如实封口径，删 `ProposalForClosedPosition` 回链读径+`ActualAPRWriter` 接口+adapter 双委托+编译断言+fake 锚测（死码全清，史实在 git）。settlement.go 挂点留注记指回链语义。
- **重开条件**：提案驱动仓位轨立项（a 路）时此桥随件重建——届时须先裁「提案→仓位」归属语义并落 schema 裁决行。

## D-531 spot_depth 产者死活裁决（D-103 收口撤除非故障）
- **死因实锤**：git blame 定点 ab59fecde（10-07）——D-103「撤 cexscan 深度采样器」随 D-578 观察窗满收口删 depth_sampler.go，产者是按计划退役非断链故障；facts 行保留=终值快照（该 commit 明示保留读面）。
- **裁定**：①接流产者**不复建**——WS book 流接产属新需求另票（D-004 禁回退 REST 轮询）；②sysretro 深度段改化石标——最新样本龄>48h 章首标「数据化石+截止日」（depth_section.go 周报表+日报行同挂）；③facts 旧行**保留**——历史只读不删（D-103 同口径）。
- **次缺陷留痕**：消费面读化石 8 天零告警=D-033 自盯未捕（残值非错值，告警判据无「数据龄」维——数据源撤除须同帧评估消费面为新增规则）。

## D-532 graduation 零合格断链裁定（取证票收口——scanned=127 qualified=0 三因分解）

- **拒因逐行分解**（SG 10-15，scanned=127=active 33+exited 窗内 94；首败归因按判据链序）：
  active——缺账 23（basis NULL 23/est_fee NULL 19，cfd 轨 25 行全缺 monitor 簿）+全账净额非正 6+天数不足 4；
  exited——缺账 68（exit 冻结收敛列 NULL：normalized 25/27、age_7d 6/6、adverse 3/3、data_lost 34/58）+净额非正 25+时长不足 1。
  **缺账合计 91/127=71.7% 为第一断链**（venue 簿/basis 列从未覆盖，非判据过严）。
- **data_lost 58 归因**（票②）：非采集死——universe last_seen 全 TRADING 新鲜、radar 每小时在跑。两簇：①24 行 binance 交割腿（BTCUSDT/ETHUSDT×4 合约各 6 次重开）——perp_delivery 轨 10-14 上线即遇 mark/index 缺帧（`futBySym` curOK 谓词不过）→miss≥3 退→重开→再退 churn；②34 行 okx×binance 山寨 perp_perp（ZAMA/ATOM/USELESS 等）——「任一侧缺席/nil」miss：funding 覆盖率缺口（binance 745/882）+对侧行间歇缺席，率源缺 accrue 跳过日志实证。
- **D-033 自盯次缺陷命中**：data_lost 58 行退出零告警（`paperExitNote` 只注 note 无 alert 挂点）；`warnMonitorBookAbsent` 哨兵 6d 窗内零推送记录——两自盯面皆未捕断链。
- **裁定**：修喂流自愈为主+仓位规模另票。①立票：cfd monitor 簿覆盖缺口→warn 推送实证接线核查（哨兵为何沉默）；delivery mark/index facts 缺帧自愈/缺即如实告警；②立票：data_lost 批量退聚合 warn（同毕业链聚合惯例，退出即推）；③qty_usd $1000→$2000+ 名义额档另票评估（accrued 22/33 已净正——费率衰减后 $1000 名义额边际，但缺账是闸门先修账）。
- **不动项**：all-in 净额判据零动（D-468 账真）；graduation 合格矩阵零动；data_lost 仍不合格集外（失联截断非证据）。

## D-533 cmc_* facts 保留裁定（暂停非废弃——业主令 10-15）
- **业主令**：CMC 数据后续要用，现忙未到时候——保留。
- **裁定**：`cmc_*` facts 8.2万行（产者 10-11/12 停写）定性为**战略数据源暂停**非死表——行不删、产者重启另候时机；审计席不得以「停写无消费者」再提清理票。重启径：需要时立票恢复产者（D-225 迁 ticks 价锚不影响保留裁定）。

## D-534 本金口径=USDT+USD（业主令 10-15）
- **业主令**：只统计 USDT 和 USD，其他币种不统计、不调用——这是我们的本金。
- **裁定**：①资本分配/可动用池/收益率分母一律 USDT+USD 计——BTC/DOGE/杂币持仓为非本金资产，不调度不折算入本金（D-528 NAV 分币桶展示照旧=披露面，非本金面）；②DCI 本金按 USDT 计但在仓期=锁定档；③USDC/BFUSD 为准稳定币暂挂（业主令字面未含）→ **业主裁定 10-15：USDC 不动不入本金口径**（binance earn USDC 466 实拍挂非本金披露行）；资金不足时业主补 USDT 而非口径扩列——D-535 重开条件随之销号。
- **影响面**：金钱不眠（D-286）idle 计量、策略本金上限、≥8% 年化分母皆以本口径为准。

## D-535 业绩基线重置（业主令 10-15——本金=11,317 USDT+USD 起，历史盈亏归档）
- **业主令**：以当前 USDT+USD 本金为基数，全新记录公司收益，以前盈亏归档从 0 业绩起。
- **裁定**：①基线锚=10-15 实拍本金 **$11,317**（可动用 10,949+锁定 368：binance DCI 358+okx 结构化 10）；②历史 NAV/income 行不删——归档保留只作考古不作业绩（同 D-533 口径）；③收益计量只认 USDT+USD 池变动——非本金资产（BTC/DOGE/杂币）涨跌与币种计息不入业绩分母分子，走披露行；④外部充提不得计损益——业绩真值=池变动−外部流净额，依赖 io+划转台账（残差对账票为前置使能件，落地前业绩序列标 provisional）；⑤D-030 ≥8% 分母随之换为 11,317（目标绝对值≈$2.48/日）。
- **重开条件**：USDC/BFUSD 若业主裁归本金则基线同帧调整（当前挂 D-534③ 待回）。

## D-536 总览页重设计（第一性原理——业主唯一问题=我的钱现在怎么样）
- **业主令**：重新设计前端总览页，本金/在仓订单/盈亏/锁定未返，决策席做主。
- **裁定**：五段信息层级=待办横幅→本金条（可动用/在息/锁定三档+非本金灰字行）→业绩条（基线以来/今日/30d年化 vs 8% 达标线）→在仓订单统一表（DCI/结构化/earn/合约/纸面跨轨列全）→风险心跳紧凑条。`getOverviewBundle` 单 RPC 一次出全段（前端零信任只渲染）。本金口径=D-534；缺显缺不显 0；provisional 挂残差对账落地前。
- **分界**：investor.tsx 报告页不动；overview.tsx 全量重写。请求箱并入总览§0（业主动作面唯此一处）。
- **业主令补（10-15）**：前端时间戳按 UTC 显示（CFD/CEX 全 UTC 世界——到期/交割/开仓误读本地时是错账源），总览页先行、fmt 组件留 UTC 径，他页同型后续统一。
## D-537 cfd/delivery 账源覆盖查证收口（取号纠撞：实施席分支按旧 master 取号撞 D-534，归位 D-537；D-532① 票）
- **取证翻案（D-532 归因修正，SG 10-15 三通道）**：
  ① **cfd 25 行 basis NULL 根因≠簿未覆盖**——`fin_xasset_monitor` 三元组实际存在且新鲜（JP225USDT-JP225|okx:perp|3001157:mt5 等 62 组实拍），但簿首行晚于 paper entry_ts（簿由 demand=universe 达标行∪active 仓派生，开仓后 ≤1 cron 轮才起写；JP225 三行簿晚入场 ~2min、NAS100/XPT 簿晚 ~1d）→ `XAssetMonitorRowNear` 的 `ts<=entry_ts` 锚谓词对簿后生行**结构性永空** → entry_basis 永久 NULL + est_fee spread 分量 NULL（fee NULL 19 同根）。
  ② **data_lost 58 行全部翻案**——exited 行 100% 带 `paperExitNote` 注记（spot 轨唯一写手签名），归因=**StepPaperSpotCarry 累积环缺 structure 过滤**：accrable 全表迭代把 perp_delivery/perp_perp/cfd_cfd/long_spot_short_perp/cfd 行按「universe 缺席」计 miss→3 连误杀（退出 ts 落 4h 扫描格点+重启轮实证）。delivery 24 行 churn=互杀非 mark/index 缺帧（premiumIndex 接线在位且每轮正常开仓）；perp_perp 34 行同此（funding 覆盖缺口只贡献 miss 计数加速）。
  ③ exited 收敛列 NULL 68=① 的 entry NULL 传播 + delivery data_lost 退路径传 nil（无 curBps 可算，诚实缺）。
- **修法（判据/矩阵零动）**：a) spot 累积环加异结构跳过（`structure∉{"","spot_carry"}→continue`，异轨行零接触）；b) cfd accrue 轮 entry_basis NULL ∧ 簿有行 → 以**首个实测簿值**补锚+同拍补 est_fee（`AnchorPaperEntryBasis` IS NULL 幂等，note 留痕「首测簿值补锚」——锚语义从「入场时点」放宽为「首个可观测」，值非臆造）；c) 哨兵补口见票②（warnActiveBookAbsent 加 entry-锚死维）。
- **perp_perp 谓词评估结论**：「任一侧缺席=miss」非过敏——funding 差收割双腿费率缺一无法算 signedNet，分档无意义；真缺口=binance funding 覆盖 745/882（fundingInfo interval 缺符号→rate 不落）另列观察面，不动谓词。
- **不动项**：all-in 净额判据零动；data_lost 冷却语义零动；簿缺仍诚实缺（补锚仅限簿已有实测行的场景）。

## D-538 喂流断链自盯补口（取号纠撞归位 D-538；D-532② 票收口——哨兵沉默归因+聚合 warn 接线）
- **哨兵沉默归因（取证件）**：`warnMonitorBookAbsent`/`warnActiveBookAbsent` 均在位且每轮跑——沉默根因=谓词纯存在性：real 授权组合仅 XAUUSD(3001157×okx) 一行且簿已覆盖（`xasset_allow_combos` enabled∧real 实拍）；active 仓簿今全在（覆盖晚到非缺席）→ 两哨兵沉默是**正确行为**非接线断。真失明维=「簿在但 entry 锚死」（`ts<=entry` 谓词对簿后生结构性永空，存在性谓词看不见）。
- **裁定**：①data_lost 批量退聚合 warn 六轨统一接线（scan spot/cfd/mtm/cex + radar perp_perp/perp_delivery——退出>0 一轮一轨至多一条 `WarnDataLostExits`）；②`warnActiveBookAbsent` 加第二维——簿在 ∧ entry_basis NULL ∧ 入场超 `paperCfdMonitorMaxAge` → 锚死 warn（与 D-537② 补锚径互为监督：该愈未愈才报）；③fail-open 吞错不改——既有 `log.Printf` 留痕已合规（查询败≠缺席，不误报优于误报）。
- **边界**：缺账判定语义零动；聚合限一条/轨/轮；锚死窗借用 15min 陈旧闸值不另造魔数。
## D-539 残差对账闭环（取号纠撞归位 D-539）（io 台账消费者=等式对账件+内部划转真台账）
- **schema 裁决**：内部划转复用 `fin_venue_io`——迁移 1029 加 `from_wallet`/`to_wallet`（快照 venue_type 键域），direction='transfer'，幂等键沿用 (venue,config_id,direction,external_id)（binance tranId / okx billId / earn `se-<purchaseId|redeemId>`）。不立新表=流量同构同一账本面。
- **划转产者**：cex-io 采集器加第三方向——binance `asset/transfer` 逐 type 枚举（20 类覆盖已建池钱包对，未列 type 漏采由残差告警自披露）+simple-earn 申购/赎回史（**D-524 申购反推升级为真台账**：申购=spot→earn、赎回=earn→spot）；okx `asset/bills` from/to 非空行（账户码 6=funding/18=unified，余如实记 acct_N）。gate 仅 sim 配置无采集账户=缺口如实记（如立 real 配置同型自动接入）。
- **对账件**：`ResidualRecon` 搭车 snapHook（快照写径同拍，零新定时器，safego 异步不阻塞写径）。范围=非保证金池（spot/funding/earn）×稳定币——perp/unified 权益随 UPL 逐帧漂移无 UPL 台账对等式必噪（UPL 台账另票）；income 仅计 symbol=ccy 原生币收益。阈=max($1 尘埃 D-529, 0.5%×equity)，越阈→critical 24h 去重带分解（prev→cur/外部流/划转/收益/残差）。
- **外部充提落点约定**：binance/gate→spot、okx→funding（venue 实际落点偏移会以残差告警暴露=自披露设计）。
- **io 求和粒度=venue 级**（io 行归 canonical config，池行按 venue 聚合——config 级会错配）。

## D-540 okx/gate 结构化产品持仓入快照（取号纠撞归位——实施席自取 D-537 撞已入库 cfd 覆盖号）
- **实证**：okx `/sfp/dcd/orders`（在仓单端点）venue 侧恒 500（Spring 错误体，带/不带 state 同败——10 USDT 在仓期间每轮 earn 轮询 first-poll error，快照只剩 savings 2117.82 漏在仓本金）；`/sfp/dcd/order-history` 同凭据 200 含全史行，本端按 state 过滤（EqualFold LIVE/PENDING_SETTLE 在仓，终态/撤单不入）。
- **同族补齐**：gate `/earn/dual/orders`+`/earn/structured/orders` 同型独立端点并入 earn 池（okx 10 USDT 零行实证同类）；binance DCI 已走 purchaseStatus 径本票不重复。
- **口径**：结构化在仓本金并入 venue:earn 池 USDT 行（与 binance DCI 混计先例同轨）；state 字段缺=fail-closed 返错不过滤。

## D-541 业绩基线锚+本金口径收益链（取号纠撞归位——实施席自取 D-538 撞已入库自盯票号；D-534/535 落地件）
- **锚键**：`app_config` 四键——baseline_usd=11317 / baseline_at=2026-10-15T00:00+08:00（BJ 历日零时，与 fin_nav_daily.day_start 同族）/ baseline_ccy=USDT+USD / NOX_CAPITAL_BASE_USD=11317（D-030 年化分母换锚）。写死改须新 D#。
- **业绩真值式**：perf = 本金池现值（USDT+USD 桶 NAV 合计）− baseline − 外部净流（fin_venue_io deposit+/withdraw−，transfer 不计）。
- **历史归档**：fin_nav_daily/income_events 加 `archived` 列，<baseline_at 行标 true——可查不入指标（行不删不改值）。
- **provisional 裁（决策席纠实施席=false 提案）**：fin_venue_io 仅覆盖 CEX 两所，**MT5 充提无 io 行**——业主级提款式事件（今 $1K 例）会成假亏损 → `perf_flow_provisional=true` 保留，MT5 io 覆盖落地后翻 false。
- **展示面**：GetFinanceOverviewResponse 加 perf_* 字段组（perf_available=false=锚缺如实缺）；investor.tsx 加本金池/基线后业绩两卡（前端零算只渲染）。

## D-542 DCI 接货卖回 LOT_SIZE 对齐 + pending_disposal 清扫（10-15 事故修——0.00433 BTC 滞留实证）
- **事故**：DCI#11 行权接货 0.00433438 BTC，`%.8f` 报量越 BTCUSDT stepSize=0.00001 → -1013 LOT_SIZE 三次尽败留仓。留仓径（pending_disposal+warn）按 D-517 正确触发，但**无清扫件**——留仓=永等人工，违「事故仓不过夜」本意。
- **修法**：①`spotMarketSell` 前置 `spotLotFloor`——exchangeInfo LOT_SIZE（stepSize/minQty）floor 报量，进程内缓存（D-004 举证：filters 无 WS 频道，低频 REST 合规）；读败/<minQty→fail-closed 留仓径不臆造。②`SweepPendingDisposals` 挂 cex_scan 拍——note~pending_disposal OPEN 行重试卖回，成则 `dci_exercise_disposal` 入账+关行+warn，败则 24h 去重 warn 不刷屏。③okx 同型——`okxLotFloor`（instruments lotSz/minSz）前置 disposeExerciseOKX。④残差对账补 DCI 交割腿——`SumDCISettleWindow` 本拍窗内 settled 行：earn 池减 outflow（本金离桶可归因）、充提钱包加 inflow——DCI 还本不再误触 critical。
- **边界**：市价即卖语义零动；取整尾零损耗如实留仓 dust；残差腿不回溯只认窗内 settled_at。

## D-543 基线测量修正——baseline_usd 11317→11360.5（okx unified 漏记补正）
- **实证**：基线 11,317 为手工清点（10-15 07:19 UTC 拍），漏记 `okx unified` 账户 ~$43.5（该户 9-20 起恒有余额）。部署后业绩式显示 +37.41 的分解：+43.5 漏记回补 +10.1 结构化入桶 −6.4 DCI#11 实亏 +0.2 利息 −0.16 浮动 → 真业绩 ≈ −6.2 非 +37。
- **修正**：baseline_usd=**11360.5**（11317+43.5），baseline_at 不变。规则加固——本金锚值今后一律取「管道路径实算值」非手工清点（快照鲜度过滤+fossil 剔除语义与读径一致）。
- **次生发现立据**：okx unified/USDT 快照行双写振荡（43.6↔3.4e-9）——两个产者交替写同键，NAV 读最新值随写序 flap ±43；binance perp 快照曾断采 8 天（10-07 化石行）。两症转票由实施席查。

## D-543R1 基线修正翻案 + 总览双计/快照错标三修（业主指正+生产 src 列实证）
- **翻案**：D-543 的 +43.5「okx unified 漏记」**不成立**——src 列取证：`okx_event`（WS 事件写）balance=3.4e-9（真 USDT availBal），`rest_calib`/`reconnect_resync` 写 19.97~43.7（Σdetails[*].eqUsd=全币 USD 总权益，挂 USDT 标签=伪账；业主确认该户全是非 U 币）。baseline_usd 回滚 **11,317**（手工清点原值即真）。
- **总览 17,199 根因**：§1 锁定档把 `fin_dci_order` filled 行 qty 叠加进总计，但 filled 行含 earn 申购单（cash_mgmt/earn_apr_lending/结构化合计 5,878）——同一笔钱已在 earn 快照桶计过 → 双计 ~5.8k。修法：总计=idle+earning（快照池即真值），锁定改披露子行「其中锁定（含于在息）」。
- **快照写径修**：`capitalview` REST 校准写行收窄到本币切片（cex 键：avail=FreeQtyByCcy[ccy]、eq=Holdings[ccy].UsdtValue 或 availBal）；MT 键 equity 回退语义保留。资金可用性 Ledger 口径零动。
- **业主令方向**：投资人面读径不能押快照可靠性——事件驱动快照（okx_event/binance balanceUpdate 已有写口）升权威源、rest_calib 降为愈合补写；事件覆盖缺口与 binance perp 8 天断采立票查。
- **R2 复发根因（10-15 部署后实拍）**：R1 判别「cex + Holdings 非空」不可靠——rest_calib 仍写 19.95=totalEq。真写者另有其人：`account_monitor.snapshotRows` 按**原始** `acct.VenueType` 判分币，okx 交易侧配置行 type=perp → 落单行路径把 totalEq 挂 USDT 写 unified 键（归一标签与判别口径不一=病灶本身）。双修：monitor 判别改用归一 vt；capitalview 判别改用归一池型（perp/delivery 单币池保总额径，Holdings 空亦如实零写不回落）。

## D-544 快照读序权威裁定——事件行优先（业主令 10-15「事件驱动升权威源」落地）
- **裁定**：`ExplorerIdleFunds`（ComputeNav/总览/日报同源池读径）同键（venue,venue_type,currency）读序钉死——`src∈{okx_event,binance_event,mt_frame}` 且 `fetched_at∈15min` 鲜度窗的事件行优先；事件源缺席或陈窗才落 `rest_calib`/`rest_poll`/`reconnect_resync`。
- **角色钉死**：rest_calib/rest_poll=断流/冷启动愈合补写，非权威源；事件行=venue 原生推送真值（okx balanceUpdate/binance balanceUpdate/MT5 帧）。
- **事件覆盖矩阵（SG 10-15 实拍）**：okx unified=okx_event 高频 ✓；binance=binance_event 低频有写径（余额变动才推——8:10 DCI 处置帧实证）；gate=无私有流仅 REST（D-206 取证在案，如实记缺口）；MT5=mt_frame ✓。
- **遗留**：binance spot/funding 面 WS 订阅覆盖未举证（user data stream 是否覆 funding 户存疑）——后续订阅面举证补齐票随排；earn/funding 等无事件频道池由 rest_poll 愈合行如实承担。

## D-545 快照表节流+保留窗（决策席 10-15——写放大 55k/日实拍）
- **裁**：①rest 系写径加 ε+心跳闸——值未变超阈且 <10min 距上次写→不落行；心跳上限 10min 钉死（fetched_at=鲜度闸证据面，去重不得造「断采」假象）；②历史保留窗 7 天——超窗删非最新行，每键最新行永留（化石池唯一真值证据）；③事件径零节流（venue 原生推送=权威历史）。
- **不建**：latest 投影表暂不建——读径已 LATERAL 化 12ms，日后退化再上。

## D-546 纸面名义额裁定——paper_qty_usd=$5,000 参数化（D-532 遗留断链）
- **裁定**：纸面仓位名义额落 app_config `paper_qty_usd`=5000（迁移 1032 种子；
  改值须新 D#）。依据三约束取 max：①真金意图额度级=本金池 $11,317×~4.4%/单仓
  （与 DCI/earn 实单量级同族）；②venue 最小名义额（binance $5 起、okx lotSz 折
  $数级）远低于此；③费率覆盖按线性模型无门槛效应（见下如实注记）。
- **如实注记（裁定边界）**：现行费率模型纯线性（fee=qty×bps/1e4），accrued 亦
  线性→accrued:fee 比与 qty 无关，**名义额调整本身不能翻转结构性净负**；本
  参数裁定的是「纸面=真金代表额」语义（统计对拍/毕业评估按实单量级记账），
  经济比修复归信号强度/持有期域（graduation 判据 D-468 如实不放宽）。
- **存量兼容**：32 行 active 纸面仓 qty 保留入场时 1000 口径（行级快照不改写；
  毕业判据按各行 qty 算仍一致），新拍开仓走 5000。
- **实现**：`scan.PaperQtyUSDAt(ctx,st)` 读 PG 键；键缺/破值→兜底 1000（失效
  安全，纸面轨断链降级可接受并注记）。

## D-547 假设裁决首批（决策席 10-15——裁决队列积压清零动作）
- **approved ×2**：#505 cfd_swap_hedged XAUUSD short×binance PAXGUSDT（D-532 取证唯一跑赢基线的对冲形态）；#7 okx FUTURES USD_UM_XPERP（品类已由 perp_delivery 纸面轨实盘覆盖——侦测→批准→在跑闭环）。**后果**：S2 票按触发条件转待领。
- **rejected ×7**：裸 cfd_swap 方向性单腿（#51/52/56/78/81/82/83 AUDJPY/BRENT/XAUUSD 等）——裸方向敞口在套利/对冲息差口径外（D-030 风控优先），同族对冲形态已批且严格占优；category_key 幂等抑重推由表结构承担。
- **留 proposed ×11**：#5 binance TRADIFI_PERPETUAL（对 MT5 指数 CFD 跨所对冲腿未取证）；其余 XAGUSD/XAUUSD 对冲变体同族未逐腿取证——待 S2 评估或后续证据。

## D-548 S2 桥复审裁（试单名义额+腿位方向——悬置项落档）
- **试单名义额**：`app_config.s2_trial_notional_usd=200`（迁移 1033 种子；本金 ~1.8%/结构——盖 venue 最小名义与费率信噪，试错损失噪音级；改值须新 D#）。钱径参数**禁代码字面量**——键缺/破值 fail-closed 拒桥（无兜底常量）。
- **复审修（实施缺陷）**：`category_key` 的 side 字段原实现只进注记未进映射——`long` 向假设会错桥成反向仓（真金白名单径方向性 bug）。修=side 进腿位映射（short=空 CFD 收 swap/多 CEX 腿；long 反之；其余拒桥+warn），`TestS2Bridge_LongSideLegs` 钉死。
- **MaxLongQty/ShortQty=5** base 单位宽松（5oz 金≈$13k 远超 gross 帽）——照准：MaxGrossUSD 先绑，腿量帽仅防单腿失衡，不视为敞口闸。

## D-549 okx DCI 资金装配——savings 赎回前置步（决策席 10-16）
- **实证**（SG）：okx USDT 全躺 earn 池（savings~2117.9+结构化 10），unified/funding 双零；`tryLegOKX` 余额源只认 unified+funding→qty=0 每拍撞约束闸（#445 存活期 critical×24 至 SLA 过期方止），结构性必复演。
- **裁定**：dci rail 申购装配链加 savings 赎回前置步——shortfall=needed−(unified+funding)>0 时赎 min(shortfall, savings_avail) 落 funding，再走既有 18→6 划转→申购。提案质量闸（D-514 溢价≥3×earn、D-526 σ界/尾损）已保证赎回换仓经济性——为更优提案腾挪所内资金=金钱不眠同向裁定，不属新风险面。
- **边界**：赎回额不放大不预赎攒资金户；结构化在仓（dcd positions）不进可赎面；savings 可赎额读败 fail-closed；提案额口径核=可装配额。

## D-550 cfd_swap_hedged S2 试单轨授权归属裁定（决策席 10-16）
- **实证**（SG 工作流审计）：假设#505（D-547 批、D-548 $200 封顶）经 S2 桥落 `xasset_allow_combos`#16（enabled+live、$200 帽）~10h——positions=0，三层封死：①`engine.go:172` `isStrategyDisabled(o.Type)` 检测侧掐产出（宿主 XASSET_CARRY 10-14 retired）；②`engine_opp.go:163` `!IsMTVenue(opp.VenueBuy)` 入口闸钉死买腿=MT 单向（side=short 桥出 buy=CEX×sell=MT 反向即拒）；③gate.Submit `IsStrategyDisabled` 兜底同拒。
- **裁定**：S2 批准试单=委员会级独立授权挂**组合行**（max_gross_usd=敞口硬顶）——宿主策略退役只冻结其存量组合域，不溯及委员会后批的试单授权；combo#16 属试单属员非 XASSET_CARRY 复活。反向腿向（空 CFD 收 swap×多 perp 付 funding）执行链须镜像完整（monitor/安全边/regime/econ/退出链）方可点火，缺一 fail-closed。
- **边界**：retired 语义不破（存量组合不复活不产机会）；试单豁免只认桥写组合行不扩散；判据额度 DB 驱动零字面量；零 LLM 零新定时器。

## D-551 复审收口单行转发格式裁定（决策席 10-16）
- **实证**：复审收口长文堆聊天不可转发交接（10-16 trial-rail 收口长段被退「转发这么长？」）；并行席已自发用 STATE.md 尾 `<!-- 复审席 -->` 注记块存细节=事实标准。
- **裁定**：复审收口尾行固定单行 `[复审收口:<票号>] <裁定一词> @<合并hash[+修件hash]>`；裁定理由/亲证要点/机检数字/待办验收锚一律落 STATE.md 尾注记块（一票一块），收口行只带裁定+hash+STATE 指针。
- **边界**：实施报审 `[施工完成]` 规不变；注记块追加不覆盖前块；退回裁定同格式，P# 清单指针指注记块不贴全文。

## D-552 毕业链授权行豁免面裁定（决策席 10-16——D-550 原则扩面）
- **实证**：`carry_graduations`#46-48（TRX short_cfd_long_perp，5.0 天窗满、net_all_in +3.9~5.6 达标 pending）；approve 径写 `xasset_allow_combos` note='D-403毕业 paper#N' 真仓行——但 `IsTrialXAssetCombo` 只认 `note LIKE 'S2桥%'`，engine.go:176 检测侧 + gate.go 提交侧双层照掐 retired XASSET_CARRY 产出 → 业主授权落笔即成死白名单行（combo#16 同型断链第二现，授权源不同）。
- **裁定**：D-550「退役不溯及后批授权」原则扩面——授权标集合={'S2桥'（委员会试单）, 'D-403毕业'（业主真金授权）}；行级窄豁免五判据（note 前缀+enabled+canonical 四元组+env+simulated 域）不变；存量组合域与未授权行维持退役掐死。
- **边界**：业主批准时点自决（行落册即驻、修部署前不点火零风险——上限/名义额在批准表单由业主定）；不动 retired 语义、不动存量组合；豁免仍行级不扩散到策略级。

## D-553 IBKR 盈透凭据在册·适配器不立项（决策席 10-16）
- **实证**：业主 IBKR 户 FPS 入金 HKD 已到账保户；NOX 现有 venue=MT5×3户+CEX×3所，$10k 基已碎片（MT 池 $1000/$3247/$0）；在册病灶=数据断供（basis NULL×9行）、资本缺口、审批闸缺账——瓶颈在链路健康非 venue 数。IBKR 适配器=行情/执行/对账/符号映射/保证金模型重施工；其新增机会集（股票借券、股指期货基差、期权）在 $10k 体量≈零，FX carry 已由 MT5 覆盖。
- **裁定**：IBKR **凭据在册·适配器不立项**——户头已保（HKD 余额无需转 USD、无闲置费、勿清零）；记「凭据在册未接线」态保留选项价值。专精裁定坐实：当前离净年化 8% 最近的工程=修断供/补资本缺口/让在册提案可批，不扩 venue 面。户内 HKD 属业主资产非系统运营金（入系统池须业主另行真金授权）。
- **边界**：立项触发条件=出现现有 venue 覆盖不了的机会实证（如股指期货基差持续优于 crypto funding、闲置 USD>阈需国债/货基承接）再立适配器票；禁为「有户」而施工；凭据/账号明细不落库表不落本文档（业主侧保管）。

## D-554（2026-10-16 决策席）仓位归组判据表种子——structure/engine_type→template 映射

- **实证**：`fin_position_strategy_map` 建表后空表=inert（@0f50113ed 挂账）；paper_spot_carry.structure 域实拍 4 值（perp_perp×85/long_cfd_short_perp×33/perp_delivery×32/short_cfd_long_perp×23），fin_opp_template 9 行活跃集名空间在案。
- **裁定**：种子 9 行已落库（SG 直栽 PG 配置域）——structure 键：perp_perp→funding_carry、long_cfd_short_perp/short_cfd_long_perp→cfd_swap_carry、perp_delivery→term_basis；engine_type 回落键：CARRY/FUNDING_HEDGE/PERP_FUNDING→funding_carry、SWAPARB→cfd_swap_carry、CROSS_VENUE→cross_venue_spot。
- **理由**：4 个 structure 值与模板机制名空间一一无歧义（perp×perp=CEX funding 收租、cfd×perp=swap 息差、perp×delivery=期限基差）；engine_type 只种旧引擎名与模板唯一对应的五行。
- **故意不种**：XASSET_CARRY（引擎名域内歧义——同 type 可产 funding_carry/cfd_swap_carry/term_basis 仓，provenance 链才是真键，种静态映射=错挂面）；PROBE（探针仓非策略持仓，unassigned 如实）；MEANREVERT（活跃模板集无对应，落未归组格）。
- **边界**：新 structure/engine_type 出现→map 无行→unassigned 自披露，种子随模板册扩展走本表追加非改码。

## D-558（2026-10-16 实施席）SWAPARB 配对等价键改锚 canonical 产品类表——broker 组名退役出产品身份

- **实证**：`broker_symbol_map.product_group`=broker 本地 MT5 CalcMode 目录分类，SG 实拍同 canonical 跨所异组（XAUUSD=110660304:cfd_leverage/15032313:fx/3001157:cfd_spot；110660304 连 BTCUSDT 现货都归 cfd_leverage）→ `sameProductGroup` 跨所配对全灭（STATE 票：发现径结构性零产出）。组值=broker 房规目录，非产品身份。
- **裁定**：等价键=`fin_canonical_product_class`（canonical→产品类直挂：fx/crypto/index/energy/metal 闭集 CHECK，迁移 1037 种子 111 行=SG `DISTINCT canonical` 全集实拍，diff 校验零差）。`sameProductClass`：两腿类同放行（同品异组放行——病灶修复面）；类异拒（真异品如 BTCUSDT×WTI= crypto×energy，470 防护语义保留）；未归类 fail-closed 拒+日志留痕（`product class unclassified`）促归类 INSERT。470 型同 canonical 现货/期货价基变体差异不再由组名预判（组名已证伪），由下游基差闸（D-169 basisAvailable fail-closed+payback 门）与 S2 试单承接。
- **选型理由**：写径归一化（ingest 按 canonical 写类进 product_group 列）被否——覆写 venue 原生分类违 §5「腿级数据只认 venue 返回值」且需回填历史行；组等价集表（(canonical,group)→类）仍以组名为半键，新组值出现即需人工裁定才放行=病灶换形复发。canonical 直挂=等价键纯锚产品类（票面明令），新 canonical 走表 INSERT 追加非改码（D-554 fin_position_strategy_map 同范式：无行→拒+自披露）。
- **降级声明**：product_group 列与采集写径（mt_symbol_product→UpdateProductGroupByCanonical）保留为 venue 分类存档面（零读口）；ListProductGroups/ProductGroupByVenueCanonical 死读口已删，后续确无消费可另票退役列。

## D-581 毕业批准拆层+全权 sizing 授权（业主令 2026-10-16 终裁）
- **背景**：`ApproveCarryGraduation` 把「策略授权」与「当下池面可行性」打包——#47 占小池 500.7%、#48 gate:perp $0 批不过，而装配径（earn 赎回）与进场活池闸在下游根本够不着，授权时序死结。
- **裁定**：①批准=授权——结构域硬闸全留（证据复核/旗标/canonical/平台），池域判据降级 decided_note 警示不拦写白名单；②进场=可行性——armed 后 equityDisciplineGate 等按活池面每拍跑，池短fall持续→自动落 fin_condition_gap；③**批准无 sizing 字段必填**——业主令「本金我出、分配公司定」：白名单行增 sizing_mode=system 档，额度=app_config 相对包络判据（pct×活池净值每拍折算，随净值自缩放）；funding_pct/usd/显式四字段降级可选覆盖。真金授权边界=策略级 yes/no（业主域）+额度上限=配置判据（决策席域，落表可调）。
- **理由**：D-184 总纲权责归位——业主=出资+回条件请求，配置权=基金管理人；固定 USD 批准额随池面陈化（批时贴线的数明天就错），相对包络不陈化；pct 零池腿批准不算基数不拒批，池回正自动生效。

## D-582 grad_system_alloc_pct 初值=0.30（复审席裁——system 档默认包络）
- **裁定**：armed 组合 sizing_mode=system 时额度=30%×min(双腿池权益)，每拍活池折算。
- **理由**：低于存量跨名义帽档（NOX_CROSS_NOTIONAL_EQUITY_CAP=0.5）不打架；显著保守于业主亲批先例（armed #16/#17 ~$700/$1000 池≈0.7）；首季观测期宁小勿大，证据攒量后决策席可调（配置判据非业主逐单域）。缺值 fail-closed skip 不误放。

## D-583 armed 白名单清场（决策席审计裁——退役死行+代理腿，TRX 三连留 armed）
- **实证**（SG 10-16 双通道实拍）：armed enabled 8 行中 #1-#4 挂 venue 277842155=broker_accounts 已拆户（永不成交死行）；#11 XAU 3001157→okx=Exness swap_free 户 swap 双 0+XAUUSDT funding≈0→**双引擎全熄只剩收敛赌注**；#16 PAXGUSDT×XAUUSD=代理腿基差赌注（KB verified 坑 `cfd-cross-venue-structural-spread` 同构——异定价源不收敛，PAXG 代币可脱锚）；#1 CLUSDT×WTI 死行+代理腿双叠加。TRX #17/#18/#19 同结构三 venue（同因子一注三票）但引擎真实（funding+10.95% 付息窗如实等待中）→留。
- **裁定**：`UPDATE xasset_allow_combos SET enabled=false WHERE id IN (1,2,3,4,11,16)` 已落 SG 在产（行留授权痕迹不删）。armed 面净剩 TRX 三连等 funding 翻负。
- **理由**：D-184 权责——策略级去留/组合构成=基金管理人域非业主域；白名单语义=「授权且可执行」——venue 已拆/引擎全熄/代理腿三类的占位行污染待入场判读且坑面行如放行=真金白银赌相关性（违 D-019 不赌第一性，代理腿≠同标的）。TRX 三连集中度风险自知但结构合法，funding 翻转后按 armed 径正常评估。

## D-584 真金门槛审计（决策席裁——pipeline 结构性洞 + dci_buy_low 停板）
- **实证**（SG 10-16 双通道）：①DCI 已结算 4 单——#7/8/10 USDT 结算利息 +$1.41/+$1.40/+$1.14=**+$3.94**；#11 PUT 83,000 被行权收 0.00433438 BTC（结算时指数 $81,300）≈**−$5.6~−7.3**——**3 赚不够 1 亏坐实，净 −$1.7~−3.4**；且残 0.00433 BTC 裸敞口（binance earn ownership_share 0.3334）违 D-019。②fin_proposal 显 dci_buy_low 全程 `proposal→executed` 同日批——**零纸面沙箱**（paper_spot_carry 只接 carry 模板）。③`xasset_allow_combos` **零出处列**——无 graduation_id/source，armed 白名单可直插绕过整个管道（#1-#4 demo 种子行实证 venue 已拆仍 enabled 直到 D-583 清场）。
- **裁定**：①`dci_buy_low` 停板（fin_opp_template.enabled=f 已落 SG）——卖 put=裸下行负偏结构，收权利金≠套利，违 D-019 第一性；`dci_sell_high_covered_call` 留 enabled 但**只许跑托管库存径**（BTC 托管线在能力账本）。②行权残余 0.00433 BTC 裸敞口处置挂条件面票（earn 赎回+USDT 化 or 归入托管仓待裁定执行径）。③真金门槛补两洞立票：armed 白名单出处约束（enable 须有 graduation/paper 出处）、全模板纸面先行不变量（任何模板上真金径前须有沙箱证据或 D# 豁免）。
- **理由**：armed 表是进真金前最后一闸，不校验血缘=管道形同虚设；卖 put 的期望结构=小赚大亏负偏（premium 封顶/下行不封顶），数学上就不是套利是卖保险；4 单样本 N<30 不引统计判据，停板依据=结构性违 D-019 非胜率。

## D-585 坑条目自问改条件触发（决策席裁——D-075 无差别 echo 摆设病灶修）
- **实证**：`pre-commit:139`「记忆系统自问」=无条件 echo——3347 commit 全印、带经验库条目仅 14 次（0.4%）；且 pre-commit 时点拿不到 commit message 做不了「修复类」判断，属挂错时点的噪音非门禁。真门禁=其上 D-130 schema 校验（只管入库条目格式，不管该不该入库）。
- **裁定**：①新增 `scripts/hooks/commit-msg`——message 含修复类信号（fix/修/bug/病灶/坑/回归/revert）且暂存面未触碰 `docs/经验库/`|待归纳 → warn 提醒（exit 0 不拦）；带条目即静默；非修复类不问。②pre-commit 无差别 echo 行删除。③D-551 收口注记块必填字段+「**坑处置：<条目指针|无坑>**」——判断位在复审收口（坑刚钉死、证据最全），commit-msg warn 只负责把时点从「每次」改成「该问时」。
- **理由**：提醒机制有效性=问对时点×有判断位。无条件 echo=3347 响 14 听的噪音；commit-msg 条件触发+收口必填字段=「该问时才问、收口时必须答」。不拦是因坑有无是判断非格式（格式归 D-130 闸），拦截会逼出凑数条目。

## D-586 经济审计验证项册建立（arb 经济经验精炼入册——业主令 10-16）
- **裁定**：`docs/经济审计验证项.md` 立册——arb 决策库经济层（D-019~028 原则面/D-061~065 验证方法面/facts.md 事实面/kb verified 坑）精炼为 18 条验证项四域：A 收益真实性（金额档利率/计价币种/息差盖费/保本天数/负偏识别/持续性窗）、B 品种身份与收敛（同标的真伪/代理腿/持续性价差判别）、C 成本计量口径（swap 归一化/3 倍日异期/申报≠实收/费用全计/计价统一）、D 验证方法与血缘（纸面先行/armed 血缘/环境-策略分离/裸敞口零容忍）。
- **理由**：arb 财产是经济面教训（71 笔跨所 CFD 全亏、阶梯利率诱饵、BABY 计价证伪）非代码面——精炼成闸位判据才进 NOX 管道；每条挂接位指明哪道闸消费，供真金门槛补洞①②票与毕业闸判据扩展直接引用（EA-xx 编号）。
- **NOX 侧增量**：EA-05 负偏结构/EA-15 纸面先行/EA-16 armed 血缘=本次审计新发现的洞，arb 无此坑（arb 无自动真金径）——双向精炼：arb 教训入册+NOX 实证补 arb 未覆盖域。

## D-587 经验库两分域+经济类事件驱动入库机制（业主令 10-16 裁定）
- **裁定**：①库两分——`docs/经验库/`=工程类（现状不变），**新立 `docs/经济经验库/`**=经济类案例层（结算亏损归因/策略陷阱/市场事实教训/结构证伪，README+模板+3 首案已入库：dci 卖 put 负偏/代理腿非收敛/funding regime 腰斩）；`docs/经济审计验证项.md`（D-586）=蒸馏判据层（EA-xx）；`~/kb/pitfalls/`=跨项目泛化层 tag 分域。②**入库时点=事件驱动非定时**——坑发生/结算亏损/审计发现时点机械产「待归纳」行（结算亏损自动钩子并入补洞②票范围），复审收口「坑处置」字段（D-585）裁定正式入库。③**LLM 判断工作者只产草稿**——聚类/起草进待归纳层，**永不自动吸收改判据**（arb practices#23 同型裁定移植）。
- **理由**：「定时 LLM 整理」=D-075 echo 已证伪的失败模式（延迟=证据散=零入库）；事件驱动把入库时点钉在证据最全的时刻。吸收权留在裁定层保决策权（AGENTS §0）；LLM 草稿层降起草成本不降判断权。

## D-588 门禁摆设化双修（业主令 10-16——行数软警告无人理+推送失败静默滞留）
- **实证**：①check-lines 软警告（>300）exit 恒 0——341/318/301/301 四个超标文件长期挂警告零收敛（engine_opp 刚压线 301 还在长），3347 commit 无人因软警告拆分过文件；②git push 瞬时失败（github SSH 断连实证 10-16）无重试无告警——commit 滞留本地=部署/复审断链。
- **裁定**：①`check-lines.sh` 软警告改**棘轮基线闸**——`scripts/check-lines.baseline` 冻结 4 存量文件当前行数：基线内只许缩不许长（+1 行即硬阻断实证 exit=1）、基线外新超标硬阻断、拆完删基线行=只减不增；②新增 `scripts/git_push.sh` 包装器（3 次指数退避+终败醒目报错非零退出）；③pre-commit 加**未推送 commit 检出**——本地领先上游即每次 commit warn 直至推送成功（事件驱动可见性，推完自消）。
- **理由**：软警告=承诺永远不响的警报（D-075 echo 同型），棘轮把「建议拆分」变成「物理上不能更糟」；推送失败属瞬时错误，重试覆盖 90% 场景，残余失败由 pre-commit warn 在下次 commit 显性化——告警原则=该响时响、推完自消、不拦但能看见。

## D-589 Stage-1 就绪取证裁定——条件属实但执行链封死，放行须先接状态机豁免
- **实证**（SG 三通道交叉 10-16）：stage1_ready 判据全绿独立重验属实——armed #17/18/19（TRX×15032313 三所，note='D-403毕业 paper#67010/11/12'）enabled，纸面行 5.5d 存活 accrued +$1.82~2.61，TRX funding 10.95% 年化读数秒级新鲜，7d 零事故仓，venue cap 10+canonical cap 2 在册，kill 关，NOX_LIVE_TRADE=true，execution_controls=PAPER enabled。**但执行链实拍**：autotrade_state=STRATEGY_EXHAUSTED（10-14 委任退役 XASSET_CARRY 触发 all-disabled 停机，人工恢复制）——armed 豁免组合过得了策略禁用双层（D-550/552），过不了全局停机层；且 `checkAllStrategiesDisabled` 只数 7 个策略类型键（全 retired→true），人工翻 RUNNING 下拍即打回=**翻不动的假开关**。就绪告警本身有覆盖缺口——stage1Checks 零执行链可达行，绿灯跨停闸发出=半个假绿。
- **裁定**：①Stage-1 授权在案（业主批 #46-48）——放行语义=armed 组合在经济面合格窗自动开首仓，非新增批准；②放行路径=票「Stage-1 放行接线」：全灭判据计入 enabled 豁免组合（armed 属员在=引擎有活干≠全灭，同 D-550/552 豁免同构），就绪表补「执行链可开」行；部署后决策席人工翻 RUNNING 留痕；③**禁复活 XASSET_CARRY**——stats PnL −63.48 在 −50 硬线下，复活即被委任闸再杀=拍振，退役台账保完整；④当前 TRX funding +10.95% 对 long-perp 属付息侧——regime 闸如实拒到条件合格，放行≠立即开仓，首笔真仓=真验收锚。
- **理由**：就绪判据管「市场/账面条件」，停机闸管「还有没有活可干」——豁免组合存在时后者口径本身错了，修判据不翻开关是因果序正确；委任退役的 −63.48 是旧管道实亏台账，宿主策略不复活与毕业组合放行两件事不冲突（D-550 原则沿用）。

## D-590 自审义务契约化（业主令 10-16——三退回路实证：机检绿≠验收锚过）
- **实证**：combo-source 三退同源病灶——①回填 VALUES 两连反（combos=17-19/graduations=46-48 凭票面转述写映射，未拿 SG 实拍对拍）；②s2_trial 枚举建三写径只接 approve 一条（delegate/s2bridge 漏接——「枚举建了≠写径接了」）；③注释描述不存在的兜底 SQL。复审席同型失误一例：裁定「approve 写径漏 Source」凭局部阅读断言（237 行实有）。**根因**：契约§3「自检」只有词无定义——实施方把自检等同机检绿，验收锚对账/生产实拍对拍无强制。
- **裁定**：自检升格**自审三件套**入 AGENTS.md——①机检绿（§6 照旧）；②**验收锚逐条对账**（报审体每锚一行 `锚=PASS+证据|未达+缺口`，零锚票显式声明「无锚」）；③**生产实拍对拍**（票面涉生产数字/id/映射的改动须亲查实拍，禁凭转述写常量映射——SG id 对位本可一次拦住两连反）。复审/决策席同责：裁定引述事实须附亲证出处，未亲证标「未亲证」。
- **理由**：三退每一伤都是「实施方自问一遍验收锚就会自己发现」的级别——缺的不是能力是流程里没这一步；同责条款=裁定层自己先立此规（上轮误判实证约束必要性）。

## D-591 arb 交付自审规约移植（业主令 10-16——D-590 之上补全件）
- **实证**：arb `AGENTS.md §7.3` 自审件=**A–F 六件清单+两时点强制+防偷懒条款**（D-045 业主两次打回「自我审计没有？」后入纪律，arb practices#21 在案）——NOX 侧三退回路（combo-source 对位两连反/写径漏接/注释虚述）=同型病灶复演：缺的不是机检是「交付前自己先按清单过一遍」这个流程位。
- **裁定**：移植落地 AGENTS.md §4「交付自审」——①**两时点强制**：方案/设计成形即先审（分发施工前），交付/报审前对成品再审；②清单 A–F 原义移植（架构复用/第一性实现/洁净/正确性对抗测/§1+硬约束合规/文档同步）；③NOX 增量 G/H 保留 D-590 两钉（验收锚逐条对账附证据、生产实拍对拍禁转述写常量）；④**防偷懒条款**原文意保留——自审是流程不是汇报材料，不待追问。复审/决策席同责（裁定引述事实须亲证或标未亲证）。
- **理由**：arb 侧同型病灶已被 D-045+§7.3 根治过（自审前置门禁化），移植即得成熟件不重造；NOX 增量保留=通用清单+本域实证两钉的合流。

## D-592 对话纪要层补立（业主令 10-16——arb §8 移植）
- **实证**：arb `docs/handoff/dialogue.md`（§8+check-out 第 4 步「本次对话落 dialogue」）419 行健在——每次实质对话追加结构化纪要（日期/参与方/议题/需求/结论/决策号），>450 行滚 LOG.md；NOX 9-30 重置（D-001）后该层未随契约过来，`docs/handoff/` 无此文件、AGENTS.md 零命中。
- **裁定**：建 `docs/handoff/dialogue.md` 并入 §2 文档清单+§4 流程条——实质对话（决策/追问/纠偏/授权类）收工追加纪要，纯执行回执不记；decisions 层只收「成了决策的裁定」，dialogue 层收「追问→纠偏」过程链（被否提案/理解修正/授权来由），两面互补不重复。历史对话不回填（工具 session 记录兜底），前向记录。
- **理由**：业主「为什么这么做」可追溯权+换会话后上下文重建都靠这层；arb 实证有效件直接移植不重造。

## D-593 全模板纸面先行不变量+payoff 分类闸（补洞② 设计裁定——D-584 审计病灶收口）
- **实证**（SG 10-16 亲证）：`fin_opp_template` 9 模板中 proposal 轨 4 条（dci_buy_low/dci_sell_high_covered_call/option_spread/term_basis）零纸面管线——`paper_spot_carry.structure` 实拍仅 4 值（short_cfd_long_perp/long_cfd_short_perp/perp_perp/perp_delivery）全属 hedged/carry 族；dci_buy_low 14 提案 4 结算净亏坐实「proposal 轨直批真金零沙箱」。提案执行唯一收口=`dciexec.Executor.GoProposal`（飞书 go N→执行腿→SetProposalStatus；earn 腿在其内、resume 续执回环同径）。
- **裁定**：①`fin_opp_template` 加 `payoff_class`（CHECK 枚举 directional_hedged|yield_on_holdings|defined_risk_vol|naked_short_vol，9 行按 id 显式回填禁默认臆分）+`paper_gate`(required|exempt)+`paper_gate_ref`（exempt 时非空 CHECK 钉死）+`paper_structures text[]`（paper_spot_carry.structure 证据键）；②`GoProposal` 前置模板闸——模板无档拒、naked_short_vol 结构性拒（dci_buy_low 落此类）、required 且 `paper_structures` 无对应 paper 行→「待纸面」拒；③豁免种子：`cash_mgmt`/`earn_apr_lending` 引 D-286 承接位（无方向敞口）、`dci_sell_high_covered_call` 引本 D#（持仓资产上卖 call=yield_on_holdings 非裸卖尾险，托管 carry 主线不可断）；`option_spread`/`term_basis` 留 required+空证据键=如实待纸面（纸面管线另票建）。
- **理由**：真金闸不能靠「记得跳过坏模板」——判据落表+单收口前置检=结构性不可绕；豁免显式 D# 可审计。

## D-594 治理权移交：真金动用+方向锚归投委会（业主令 2026-10-16）
- **业主令原文**：「批真金动用、定方向锚这两项都交给公司负责人去，不应该是投资人的事务」。
- **裁定**：投资人域收窄为**出资/撤资 + 条件请求回执（凭据/权限/约束喂入）**两事；真金动用授权与方向锚变更移交投委会（决策席）全权裁定并背账——投资人接口只剩「条件请求响应」，提案批准从「业主飞书 go N」改为**决策席授权制**（执行 UX 改造另票）。§1「方向锚修改须业主明示」同步修订为「投委会 D# 裁定+业主持有撤资/否决退出权」。
- **理由**：业主已明示投资人职责=出钱+满足条件请求；真金判断与方向裁定属基金管理人本分——业主亲自裁决每笔真金动用恰是 D-184 分工的反例。
- **对价约束（决策席自律，因移交而更重）**：真金授权裁定必带反证锚（什么证据=本裁定错）+到期自验行；裁定→盈亏归因表机器滚动（连败自动告警升级）——两配套件随 D-594 同批立项。

## D-561（2026-10-16 实施席）裁定归因簿 fin_decision_outcomes——扫描幂等物化+30d 结算归因+连败 warn 边沿（决策席问责机制②系统件）

- **选型**：新表非复用 position_settlements——结算簿=每仓一行事实账（position_id 键），裁定=决策事件（类别×对象键），粒度/键域/生命周期不同，复用=决策语义错挂结算行。
- **裁定**：物化径=扫描幂等（UNIQUE(category,object_key) ON CONFLICT DO NOTHING）——裁定源行（carry_graduations approved / fin_proposal executed@dciexec 两写点）本身携带全部决策事实（decided_by/decided_at/structure/combo_id），单漏斗零漏路径，部署首拍回填存量已批行（首批裁定行落库实拍=免等新裁定）；30d 归因=positions（开仓∈[decided_at,+30d] ∧ strategy_key 匹配 ∧ canonical 段窄化）JOIN 其后 settlements 净额；告警=累计净亏 ∨ 连败≥3（票面定阈值）→ warn **边沿推送**（alert_state 态机防刷屏，D-389 先例）。归因是呈现非裁决——零自动改判据/零自动禁用（禁线遵行）。
- **类别域**：graduation_approved（人工 RPC+delegate:D-513 双径）/ proposal_go / gate_exempt（枚举预留——现码无豁免写径，落地即入册不改码）。
- **实施事故留痕**：迁移初版撞 1038（D-581 sizing_mode 已占）→ 测试库 schema_migrations 记 1038 跳过 GradSizingMode → 23 席位库 sizing_mode 列缺失连坐假绿；修=改 1040+批删席位库 1038 版本记录重放。教训：**新迁移开工先查 migrate_table.go 尾部已占号**（master 高频并线，开工时空闲≠合并时空闲）。

## D-595（2026-10-17 决策席）开工/收工协议入契约——arb §6 移植（业主令「arb 的值得整合过来」）

- **裁定**：AGENTS.md 加 §4.0——boot 四件（契约→STATE 票行→相关 D#/模块页→取证/评估 skill）、私有记忆清扫强制项（arb P1 通道不变量同义落地：会话蒸发=事实对项目不存在）、check-out 六步（自审→STATE 翻态→记忆清扫→dialogue→机检→commit+push）+「不写=失职/未提交=中断信号」牙口。
- **NOX 适配**：派单模型=每票新开会话——私有记忆清扫对 NOX 比 arb 更刚需（实施会话结束即蒸发）；check-out 尾部收敛到 `git_push.sh`（D-588 本地领先未推断链实证）；dialogue 口径沿用 D-592（实质对话才记）不双写。
- **实证钉**：迁移撞号四连（1040 双占）+btc 残余陈账误读——两案病灶同源=教训/实数散在死会话未落共享层。

## D-596（2026-10-17 决策席）时间单历元——落盘时戳强制区标（业主令「日期时间决定开收盘」）

- **病灶**：决策席自查实锤——无内生时钟，日期靠会话开头印象沿用；STATE 注记混用 `10-16 02:40Z`（UTC 标）与「复审席 10-17」（本地裸标）两历元，跨日会话必错位。业主升格：时戳错=开收盘错=决策错，非格式洁癖。
- **裁定**：AGENTS.md §5 加「时间单历元」条——一切落盘时戳强制区标（`MM-DD HH:MMZ`/`YYYY-MM-DD HH:MMZ`，UTC 单历元对齐 SG 实拍域）；交易时序判定用 venue 原生时或显式 UTC；会话日期以 `date -u` 实拍为准禁凭印象；存量混历元不回溯改标（不可考=编造），向前生效。

## D-597（2026-10-17 决策席）席位声明+失守第二形态——业主令「角色职责/项目总纲总被忘」

- **病灶**：决策席自证实锤——本轮两次把职权内事推回业主（「要派就说一声」「要落就一句话」）=D-520 问句上行的变体形态；根因=席位靠开工指令首认后不再锚定、总纲不复述=跨会话漂移。
- **裁定**：①AGENTS.md §4.0 boot 加⓪「席位与方向声明」——开工首条输出写本席/本票目标/与 §1 总纲（≥8%·闭环·硬线）对齐点，声明不出=未 boot（移植 arb 交接负载「方向校验」件——NOX 漏移即病灶源）；②§3 决策席纪律加第二形态条款——职权内事推回业主与问句上行同罪，派票/立项/裁定/收口径行，拿不准走复审席方案核验不上行；输出前自检「本句是否把该我裁的事变成业主的题」。
- **配套**：交易时序审计票立项（业主令①——MT 服务端时域/日界判定/结算到期归属三面扫，票面在 STATE）。

## D-598（2026-10-17 决策席）earn 承接位提案自动执行——autoexec 判据列豁免族种子（眠钱破口实证修）

- **病灶**：`earn_apr_lending` 提案 #572/#573 超龄 expired、#643 躺 26h——D-286 豁免族承接位提案走「飞书 go N」人工闸，委员会无飞书面+业主不操作=眠钱提案自然死亡，豁免只通了「可不可」没接通「动不动」。
- **裁定**：`fin_opp_template.autoexec` 判据列（DEFAULT false）——earn_apr_lending/cash_mgmt 种子 true，落库即 GoProposal 执行跳过人工 go；非豁免族 autoexec=true 物理拒（与 paper_gate exempt 联动判据）；质量闸零放宽（APR 复验/坑表/余额照旧，自动≠劣单放行）；提案超龄 expired 对 autoexec 族不适用。
- **反证锚**（D-594 问责件）：若承接位提案出现「自动执行了一笔不该有的申购」（如余额误读/产品 APR 已跌出复验仍买）=本裁定错——30d 归因簿盯 autoexec 类提案净额。

## D-599（2026-10-17 决策席）arb vs nox 资源裁定——NOX 为主线，arb 转维护态存续

- **裁定**：开发资源全投 NOX；arb 不再新功能开发，只做存续维护（坏了才修）+ 坑条目持续萃取喂 nox 经济库。arb 本金（20万 RMB 保本凸性/稳定币基档）**不动不迁**——持有型仓位不需要工程投入，买入持有即其终态。
- **理由**：①arb 瓶颈结构性不可修——形态=监控+人工下单，改进 arb=在 arb 重建 nox（重复建设），只改监控面=收益不动的工程；②arb 最值钱产出（坑库学费）已榨取进 nox 经济库+硬拒闸；③nox 闭环 90% 建成，边际工程回报高一量级；④arb=已证实下限（~3.5% 实跑），nox=未证实上限（≥8% 目标）——下限存续、上限攻坚。
- **回访触发**（判据写死非感觉）：nox 真金归因簿满一个考核周期且净年化轨道 ≥8% → 议 arb 本金部分迁入 nox 托管轨；反之证不能 → nox 转纯研究壳、arb 保守轨维持。
- **反证锚**：若 arb 存续期出现「该修没修致损」（采集断/账本错无人理）=维护态定义失守——维护含事故响应不含进化。

## D-600（2026-10-17 决策席）默认授权+行文 lint——角色失守根治（D-520 续钉）

- **病灶**：决策席三次同型失守——职权内事项向业主行「建议/要不要/等回执」式文（派票权、阈值裁定、收工协议改版皆中过）。根因：规矩在读层（boot 读一次）不在写层（行文瞬间无闸）。
- **裁定（写层机械闸两条）**：
  - **默认授权条款**：业主域自 D-594 起只剩**条件请求回执**一件（真金授权/方向锚变更均已归决策席）——此外一切事项决策席行文**只许「已决/决定/已立票/已部署」**，禁「我建议/要不要/请示/待回执（非请求箱件）」句式。「建议」句式只许出现在面向实施席的票面设计段，永不面向业主。
  - **行文 lint 自审**（§4.0 收工六步第①步加子项）：收工时扫本会话全部行文——凡职权内事项出现建议/请示句式=失守，**自纠重述为裁定**并记 STATE 尾注失守计数（计数器=可审计威慑：失守 N 次落一行）。
- **边界**：条件是**系统向业主提**的走请求箱机制（本就机制化非行文）；业主主动讨论时给判断+裁定行文不变——讨论≠递裁量权。

## D-601（2026-10-17 决策席）XMR 入金盲区：供应商 deal 端点请求径+provisional 续持

- **实证**：110660304（XMR）$1000 注资 fin_venue_io 零行——XM 入金落 MT5 deal 面（DEAL_TYPE_BALANCE），mtapi RPC 面无 DealHistory/GetDeals 端点（proto 28 rpc 仅 order/price 族）；ICR/EXR 两户入金走 order 面正常落表——**供应商 API 面缺件非采集逻辑缺件**。
- **裁定**：①A 径=请业主向 mtapi 供应商提 deal 史端点需求（先例：RequiredMargin 10-05 供应商实装）——已落请求箱 #79（kind=permission）；②过渡=`perf_flow_provisional` 维持 true 至端点落地+注资行补齐；③B 径（equity 差分反推入金）不建——推断账伪风险不配收益；④盲区期间业主注资须回执申报金额（请求文本已注明）。
- **反证锚**：若 mtapi 侧证实存在 deal 面读径而我们 grep 漏检，或 XMR 入金实际可走 order 面（供应商配置项）——本裁定前提塌，改走采集径票。
- **复盘点**：端点落地+fin_venue_io 首行 mt5:ticket 落库时摘 provisional。
