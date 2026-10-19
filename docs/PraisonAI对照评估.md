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

---

*评估依据：[PraisonAI GitHub](https://github.com/MervinPraison/PraisonAI)（2026-10-19 访问），NOX 产线实况（本会话 25+ 票复审实证）。*
