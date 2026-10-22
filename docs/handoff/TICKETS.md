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
