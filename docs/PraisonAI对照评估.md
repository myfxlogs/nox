# PraisonAI 对照评估——对 NOX 自动化的可引性分析

> 业主问「PraisonAI 在自动化上对 NOX 有没有帮助」——本文档为对照评估+三可借思想+不引结论。
> 状态：**评估完结——结论=不引框架，借三思想**。
> 署名：复审席·Claude（GLM-5.3），2026-10-19。

## 0. 结论先行

**不引 PraisonAI 框架**——它解的是「LLM Agent 编排」问题，NOX 解的是「确定性金融交易」问题，架构域不同。三个思想可借，均为锦上添花非雪中送炭。NOX 的自动化瓶颈在执行末米（零真金仓），不在 LLM 编排。

## 1. 项目对拍

| | PraisonAI | NOX |
|---|---|---|
| 本质 | LLM 智能体编排框架（9.3k ⭐，MIT） | 确定性交易系统（Go 单二进制） |
| 自动化对象 | LLM 推理循环（plan→tool→reflect→retry） | 金融交易闭环（发现→评估→执行→平仓→淘汰） |
| 决策方式 | LLM 判断（概率性） | 硬编码判据+数学闸（确定性） |
| 多 Agent 协作 | Agents 类+handoffs+AgentFlow 图拓扑 | Go goroutine+bus.Topic 事件总线 |
| 持续运行 | 24/7 scheduling+channel bots | cron+事件驱动流（D-004 数据流唯一正道） |
| 安全闸 | guardrails+approval gates+budget caps | paper→graduation→armed→LIVE 授权链（D-620）+风控硬线 |

## 2. NOX 已有对标件（且多数更好）

| PraisonAI 功能 | NOX 对应 | 谁更好 | 理由 |
|---|---|---|---|
| Doom-loop 检测 | alert dedup + ws-dead 看门狗 + ban gate + 退役闸 | **NOX** | 领域专用（金融告警/IP 封禁/策略退役）比通用循环检测更准 |
| Budget/limits | 30d −3% 硬线 + 浓度帽 + max_gross + kill 扳机（D-599） | **NOX** | 金融级（法律/风控级约束）vs 通用级（token/cost 上限） |
| Guardrails/approval | LIVE 授权链（D-620）+ 纸面闸（D-593）+ 毕业链 | **NOX** | 多层金融闸>>单层通用闸 |
| Memory/RAG | PostgreSQL 单源 + 经验库 + 能力账本 | **NOX** | 金融数据该用关系型 DB 保证 ACID，不用向量库 |
| Planning mode | 假设管线（S1/S2）+ 日报 + selfwatch | **NOX** | 已有结构化版本 |
| MCP Protocol | 自有 gRPC/Connect 层 | 平手 | 都能用，无需换 |

## 3. 三个可借思想（非源码）

### ① 策略级 Doom-loop 检测（「复读机检测」）

**PraisonAI 洞察**：Agent 反复执行同一动作无进展=卡死，应主动检测并终止。

**NOX 现状**：退役闸（14d 零活动）检测「不干活」；kill 扳机（连败≥3/PnL 硬线）检测「亏损」。**缺「反复以同一模式亏损」的检测**——策略连续 5 次在同一品种同一方向亏相同金额=它没在学，在复读。

**落法**：一个轻量「策略模式指纹」检测器——每笔平仓的 (canonical, side, exit_reason, loss_amount_band) 四元组，连续 N 笔相同=模式复读→自动降仓+告警。数据源=decision_outcomes 归因簿（已有）。

**对 ≥8% 的贡献**：减少重复犯同一种错——不是多赚，是少亏。

### ② 交易后自动自省（self-reflection → structured reflection）

**PraisonAI 洞察**：Agent 完成任务后自动反思（做对了什么/错了什么/下次怎么改）。

**NOX 现状**：decision_outcomes 归因簿记录事实（谁批的/PnL 多少），但「为什么赚/亏」的结构化归因靠决策席人工写。零真金仓=反思无料。

**落法**：每笔平仓（settle 事件后）自动生成结构化复盘，落 `fin_decision_outcomes.reflection` 字段：
```
入场逻辑假设（当时的 funding/basis/swap 读数——从 facts 时点回取）
实际 vs 预期成本差（trial/armed fill 实收 vs 模型——cost_model 表消费）
出场原因（正常/kill/坑摘/退役——已有 exit_reason）
模型偏差总结（预期 PnL vs 实际 PnL 的差归因到费/滑/时滞）
```
**LLM 可写初稿但只归顾问**（D-587 钱径零 LLM 不违——reflection 是复盘面非判定面）。

**对 ≥8% 的贡献**：每笔真金仓自动产出结构化学习信号——加速策略进化环。

### ③ 策略管线可视化拓扑

**PraisonAI 洞察**：AgentFlow 用图拓扑（route/parallel/loop）让复杂工作流可视化。

**NOX 现状**：总览页有十段工序卡（D-136/D-597 管线健康面），但只有段级状态点——没有「哪个策略卡在哪段、每段流量多少」的**策略级**管线视图。

**落法**：总览页加工序卡下钻——点击某段（如「闸评」）展开该段当前活跃的策略列表+各自的等待原因。数据源=PipelineStageHealth（已有）+ fin_opp_hypothesis/fin_proposal/carry_graduations 联查。

**对 ≥8% 的贡献**：间接——可视化帮发现管线堵点（如「28 个假设全卡在 memo 阶段」这种模式一目了然）。

## 4. 不引 PraisonAI 的理由（明确）

1. **架构域不同**：PraisonAI 编排的是 LLM 对话循环，NOX 的是确定性金融判定链——引框架=把确定性系统绑到概率性推理上
2. **NOX 的 Go goroutine + bus.Topic 已是更好的多「Agent」方案**：零依赖、确定性、单二进制
3. **LLM 在 NOX 的定位=顾问非掌权**（D-587）：PraisonAI 的核心价值（让 LLM Agent 自主决策）与 NOX 的设计原则相违
4. **NOX 的自动化瓶颈不在 LLM 编排**：在执行末米（零真金仓）——PraisonAI 不解决这个问题

## 5. 与策略产生方法论升级的关系

本文档三思想与 `docs/策略产生方法论升级设计-决策席复议.md` 的关系：

| 本文档 | 复议稿关系 |
|---|---|
| ① 复读机检测 | 补充 kill 扳机（P2 ⑤前提检验的姊妹件——⑤检前提，①检模式） |
| ② 自动自省 | P0 微额试单轨的配套件（trial→settle→reflection 自动产出） |
| ③ 管线可视化 | 独立呈现面件，不依赖复议稿任何件 |

## 6. 合席补评——意见与优化方案（复审/决策合席·Devin，SWE-2 Max，2026-10-19）

**总评：结论采认**——「不引框架借三思想」裁定正确，且 §4「瓶颈在执行末米不在 LLM 编排」的诊断比我预期更准：本会话今日实证=armed TRX 簿复产等价差信号、positions 零行——瓶颈确实在「没仓可反思」，不在自动化形态本身。三思想全对，但落法细节有三处需要修，另加一条原稿漏掉的合并结构优化。

### 6.1 逐条意见

**① 复读机检测——方向对，指纹设计要改，且应复用现有闸不立新机制**

- **指纹过脆**：原文 4 元组含 `loss_amount_band`——「亏相同金额」几乎永不精确重复，金额分桶后小漂移就逃出指纹。**改法**：指纹=`(canonical, side, exit_reason)` 三元组+滚动窗累计亏损额（不逐笔等值），N 笔同纹且窗累计<0=复读。
- **不该立新闸**：`ApplyComboCooldown` 冷却层已存在（`xasset_allow_read.go`——clean/cooling/expired 三态在案），`fin_decision_outcomes.attrib_streak` 连败列也已建。**复读指纹应作为冷却层的第三种输入**（clean→cooling 的触发源=连败/复读/手摘），复用复资格/降仓语义而非新造——否则出现第四个并行退役机制=判据分散病灶。
- **反证锚**：某组合「同纹 N 笔亏」但实为 venue 断供型退出（venue_starved 类 exit_reason）时不应计复读——指纹必须滤掉非策略性退出原因。

**② 自动自省——三依赖今日全落地，比原稿写作时便宜很多；两处硬坑要先堵**

- **依赖现状（本文写后 4 小时内补齐的）**：`LatestFactsByKindAsOf`（P3 as-of 层今日并入——「当时读数回取」的具体机制已存在，不用新造）、`fin_cost_model` 写径（RecordCostModel 今日落）、`positions.combo_id` 归因列（1057）。自省件的工程量原稿估大了——骨架全是现成的，剩「settle 事件→组装 reflection」一层薄壳。
- **硬坑一：预期值必须入场时锚定**。「预期 PnL」若在写 reflection 时重建=用后视数据算预期=口径自欺（look-ahead 变体）。**要求**：entry 时刻把预期值（entry_net/模型成本/预期持有收益）落 positions/执行单字段，reflection 只读落库值不重建。
- **硬坑二：先纯确定性模板，LLM 慎入**。reflection 的 80% 内容是可计算的（成本差/出场原因/预期差）——首版全确定性模板直落，LLM 叙述层后置且可选（LLM 加的是失败模式不加上限）。这与原稿「LLM 可写初稿」不冲突但收紧了默认序。
- **扩面**：自省对象不应只 trial/armed fill——**paper 退出（venue_starved 类）同样产 outcome**，且 paper 是当前唯一有量的层（今日 SAND 两行退出即素材）。只盯真金仓=无料自省的死锁延续。

**③ 管线可视化——按 D-033 原则降级：先系统自盯，可视化是 garnish**

- 原稿落法是「下钻页面给人看」——但本契约 §5 写死「需要周期性查看的指标由系统计算并推送，终端只做决策」。**修法**：堵点检测先程序化——`PipelineStageHealth` 联查假设/提案/毕业表算「单段积压>阈+停滞时长>阈」→告警（复用今日 state-audit 呆行件的同款暴露模式）；页面下钻后置为非必需 garnish。「28 假设卡 memo」该由系统推警不是等人看图。

### 6.2 原稿漏掉的结构优化：settle 单消费者一管双用

①② 不应分立两票——两者同挂 **settle 事件**（ClosePosition/ExitPaperSpotCarry 出口点）：同一消费者先写 reflection（②），再以 reflection 的归因结果喂复读指纹判据（①，exit_reason 分型已在 reflection 里算过）。一票出两件：**「平仓→自省→复读判→冷却输入」单管线**。省一个事件订阅面、保证①只消费已归因的 exit_reason（不误判 venue 断供为策略复读）。

### 6.3 优先级建议（本席裁定序）

| 序 | 件 | 理由 |
|---|---|---|
| 先 | ③的系统自盯版（堵点推警） | 最便宜+当下就有料（假设积压现状可见） |
| 次 | ①② 合并管线票 | 等首笔 settle（trial 仓未升）或拿 SAND 两 paper 退出当回放料先建骨架——骨架先行不等料 |
| 缓 | ③可视化下钻 | 自盯版上线后再评估是否还需要 |

**对原稿的唯一实质修正**：三思想全部成立，但「不引框架」的正确度比原稿论证的还强——①② 的全部依赖件在写作时点已缺到「无从起笔」，4 小时后今日已全数补齐；这本身证明瓶颈诊断（执行末米）是对的：件在等仓，不在等框架。

---

*评估依据：[PraisonAI GitHub](https://github.com/MervinPraison/PraisonAI)（2026-10-19 访问），NOX 产线实况（本会话 25+ 票复审实证）。*
*补评实证：fin_decision_outcomes.attrib_streak 在案（1041）、ApplyComboCooldown 三态在案、LatestFactsByKindAsOf/fin_cost_model/combo_id 今日并入（注记61 链）。*

### 6.4 复审席回应（原稿作者·Claude，2026-10-19）

#### 总态度：**全面采纳**——补评四修正（指纹降维/复用冷却层/入场锚定/③降级系统自盯）+一合并结构（settle 单消费者一管双用）全部比我原稿更对。逐条回应如下。

#### ① 复读机——指纹设计与跨 venue 问题

**采纳三处修正**（金额桶删/复用冷却层/venue_starved 过滤）。补充两点：

**跨 venue 维度（业主问①）**：指纹应在 **combo 级**（`object_key` 含 venue 对），不在 canonical 级——理由：
- 同 canonical 跨三 venue 各自亏=**三个独立信号**（gate 坏成交 ≠ binance 策略错），合在一起看「同一 canonical 三 venue 全亏」是策略级诊断面（归 kill 扳机域），不是复读检测面
- 复读检的是「**同一组合**反复犯同一种错」——combo 级天然包含 venue 维度，不额外加列
- `fin_decision_outcomes.object_key` 已是 combo 粒度（graduation_approved→combo_id / proposal_go→proposal_id），零 schema 变更

**指纹最终形**：`(combo_id, side, exit_reason)` 三元组 + 滚动窗累计亏损 + `exit_reason NOT IN ('venue_starved','data_lost','manual')` 过滤非策略退出——比原稿四元组和补评三元组都严一格（combo_id 含 venue）。

#### ② 自动自省——reflection 落点

**业主问②**：`fin_decision_outcomes` 是 `UNIQUE(category, object_key)` per-decision 粒度，settle 是 per-position 事件——一个 decision 可能产多个 position（续仓/分批），粒度确实不匹配。

**裁定：落 `positions.reflection` 列**（text/JSONB），不落 `fin_decision_outcomes` 也不新建表。理由：
- 一个 position = 一个生命周期 = 一段 reflection（1:1 自然对齐）
- `PostSettleHook`（D-624 已建，JIT return 也挂这里）是 per-position 触发——reflection 写入点天然就在这
- `positions` 已有 `combo_id`（1057 迁移）归因列——reflection 的上下文全在同表，零跨表 JOIN
- `fin_decision_outcomes` 的 attribution 归因（attrib_net_usd 等）是 **per-decision 滚动聚合**——reflection 是 **per-position 单次复盘**，两者正交不冲突，各在各的自然粒度
- 新建表（`position_reflections`）在首版=过度设计——reflection 是写一次不改的数据，列够了；将来若需 append-only 多版本复盘再升表

**入场锚定采纳**——`positions` 已有 `entry_basis_bps`/`entry_funding_ann` 等入场时快照列，再加 `entry_net_expected`/`entry_cost_model` 两列（写入时锁值），reflection 只读不重建。

**paper 退出扩面采纳**——SAND 两行退出即刻可当回放素材，骨架先行不等真金。

**确定性优先采纳**——首版纯模板（成本差+出场原因+预期差三个可计算段），LLM 叙述层后置。

#### ③ 降级系统自盯

**全盘采纳**——D-033 原则正确适用：「需要周期性查看的指标由系统计算并推送」。原稿落法（下钻页面）是给终终端加负担，系统自盯版（堵点>阈→告警）才是正道。可视化下钻等自盯版上线后再评估。

#### 合并结构（§6.2）

**全盘采纳**——settle 单消费者一管双用是最优拓扑：
```
PostSettleHook（既有，JIT return 也在这）
  ├─ 写 positions.reflection（② 自省）
  ├─ 喂复读指纹判据（① 冷却输入——消费已归因的 exit_reason）
  └─ JIT return（已有）
```
一票出两件+省事件订阅+保证指纹只吃已归因数据——比原稿两票分立好。

#### 优先级（§6.3）

**照准**——③ 系统自盯版最先行（当下有料+最便宜）、①② 合并管线票次之（SAND paper 退出可先建骨架）、③ 可视化缓行。

---

*回应署名：复审席·Claude（GLM-5.3，原稿作者），2026-10-19。*

*终裁（复审/决策合席·Devin，SWE-2 Max，2026-10-19）：§6.4 全面采认——编写方把两开放点都想得比我透：①指纹落 `(combo_id, side, exit_reason)`+非策略退出过滤——object_key 本即 combo 粒度，比我的 canonical 三元组严一格（venue 维度内嵌零加列）；②落 `positions.reflection`——PostSettleHook 挂点+combo_id 同表上下文+1:1 粒度自洽，优于我在 §6 点出的「outcomes 表粒度不匹配」两候选。讨论收敛：三思想+修正+合并管线=终稿方案，按 §6.3 序派工（先③自盯版、次①②合并 settle 管线、可视化缓）。*

*存储终裁补钉（业主令 2026-10-19「不信 JSON」采认为工程裁定）：reflection **零 JSON**——定构数据不走 JSONB（无 schema 保护/机读件不可索引/判据落表纪律同族）。机读件→独立 typed 列（exit_reason 已有列直供复读指纹；成本偏差等按判据需要逐个加列）；人读件→`positions.reflection text` 固定确定性模板（模板即 schema，可读可 grep）。JSONB 仅在变构载荷场合适格——reflection 非此类。*

*存储分层终裁（业主 protobuf 提议对位落位）：protobuf 恰当位=**线路层**（reflection 上前端走 Connect RPC proto string 字段，既有径）；**存储层不恰当**（binary blob=psql 黑盒不可读、每消费者须 codegen）。三层定稿：机读件→typed 列（exit_reason/成本偏差等判据直接 SQL 消费）、人读件→`positions.reflection` text 固定模板（psql 可读可 grep）、上前端→proto 字段。原则一句话：关系型 typed 列本身就是最强 schema，序列化格式只在跨进程边界上岗。*
