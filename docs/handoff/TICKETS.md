# TICKETS.md —— 票文广播件（D-651 B.2②）

与 STATE.md 分离的单行票文广播道：STATE 行保持审计档本位（禁拆散——其审计粒度是特性），
本件供实施席开机 `git pull && 读尾行` 取新票（防 STATE 大表漂移致「新行未见」）。

## 规约

- 追加只写**单行票文**：`| <票名> | <开工指令原文> | <STATE 锚=行号或注记号> |`；
- 状态翻转/核验/收口**仍写 STATE 行**——本件不回写；
- 只增不改（历史行禁回改——漂移即广播失效）。

## 票文流

| 票名 | 开工指令 | STATE 锚 |
|---|---|---|
| 告警「需确认/纯知会」两级语义返修 | `[角色:实施] 告警两级语义返修（读 STATE 本行——isReceiptAlert 匹配改 Contains 子串钉真实文案，验收加锚生产文案逐条对拍；无 skills 域）` | STATE 票表行（退回返修态） |
| dciexec 现货参考价 venue 化复审 | `[角色:复审] dciexec 现货参考价 venue 化复审（读 STATE 本行——施工完成待复审，独立重跑机检+diff；无 skills 域）` | STATE 票表行 |
| term_basis quoteFor venue/流动性/具名修 | `[角色:实施] term_basis quoteFor 过滤修（读 STATE 本行——venue 过滤+流动性地板+近到期剔除+合约具名；无 skills 域）` | STATE 票表新行（D-656） |
| 经验库待归纳 high 坑正典批 | `[角色:实施] 经验库 high 坑正典批（读 STATE 本行+D-618——待归纳高危件即升条目+noxkb gen 重生；无 skills 域）` | STATE 票表新行 |
| go 口令执行回执审计行 | `[角色:实施] go 执行回执审计行（读 STATE 本行——GoProposal 终态落审计+双轨透出；无 skills 域）` | STATE 票表新行 |
| custody_share:BTC 资产份额判据消费面 | `[角色:实施] BTC 资产份额判据消费（读 STATE 本行——custody_share:<ccy> 键读面+covered_call sizing 双闸取小；无 skills 域）` | STATE 票表新行 |
| 提案轨 jit 缺资归集前置（P1） | `[角色:实施] 提案轨 jit 归集前置（读 STATE 本行+D-647/D-658——GoProposal 缺资→JitSweep 复用件前置；无 skills 域）` | STATE 票表新行 |
| monitor 簿/coverage 联查 venue 方言键归一（P1） | `[角色:实施] monitor 簿键归一修（读 STATE 本行——MarketCanonical 联查键三消费面+存量迁移；无 skills 域）` | STATE 票表新行 |
| roster 心跳未登记 main:tradifi-syms | `[角色:实施] roster tradifi-syms 心跳排查（读 STATE 本行；无 skills 域）` | STATE 票表新行 |
| symdig 提案 AI 输出截断 | `[角色:实施] symdig 提案输出截断修（读 STATE 本行；无 skills 域）` | STATE 票表新行 |
| 纸面水位伪损行排查（01-01 ×1 $-4.65M 伪值） | `[角色:实施] 纸面水位伪损行排查（读 STATE 本行——01-01 行写入源+聚合 sanity 闸；无 skills 域）` | STATE 票表新行 |
| funding_carry quoteFor 同伤修（1414% 伪 APR 复发） | `[角色:实施] funding_carry quoteFor 同伤修（读 STATE 本行+term_basis 修票 @2285af9c0 同配方；无 skills 域）` | STATE 票表新行 |
| 前端待授权面语义分层 | `[角色:实施] 待授权面语义分层（读 STATE 本行+go 审计行票——proposed 两义分层渲染；无 skills 域）` | STATE 票表新行 |
| roster 台账建模错位修（7 员假离线） | `[角色:实施] roster 建模错位修（读 STATE 本行——任务型 spawn 分型+条件门注册同步；无 skills 域）` | STATE 票表新行 |
