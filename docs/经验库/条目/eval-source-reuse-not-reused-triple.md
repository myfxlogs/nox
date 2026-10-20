---
schema_version: 2
id: eval-source-reuse-not-reused-triple
kind: pitfall
risk: high
status: verified
keywords: [eval, 报价源, slippage, fee, fin_cex_depth, 复用未复用, 证据链]
scope: [oppmatch, evaluation, data-source, review]
summary: 报价/评估补源前必查 fin_* 既有表全谱+鲜度——「无源」误判三连（index_price 死源顶替活表/fin_cex_depth 在产被报无/纸面 realized fee 在案报鸡生蛋），复用未复用=同族病灶第三例起即系统性
first_seen: 2026-10-20
last_seen: 2026-10-20
---

## 评估证据链「无源」误判三连——既有表全谱未查即报缺

- **触发**：eval/quoteFor 补源票实施席沿「该词无源」假设施工，复审/产线实拍逐次证伪。
- **三袭实证**：
  - cross_venue 报价源初版用 `index_price`（全史仅 binance 单源=结构性死源）——真源 `fin_cex_spread` 在产跨所双向被漏用；
  - eval 五词票报「无每 venue 盘口深源」如实 missing——`fin_cex_depth` 三 venue 52 万行龄 1 分钟在产（bid1/ask1+mid_depth 齐），slippage 其实可喂；
  - funding_carry fee 报「PERP_FUNDING CLOSED 零行鸡生蛋」——`paper_spot_carry` perp_perp 154 退出行 realized 估费全量在场，纸面轨可绕行（paper 口径标后合法）。
- **修法**：补源票开工前先跑「表清单+鲜度」两查（`information_schema` fin_% 族+`max(ts)` 龄），报「无源」须附该两查结果为证——协议面取证规约同族（宣称能力缺失前须消息体全文核证）。
- **教训**：①「无源」是**待证命题非默认事实**——生产表先于印象；②证据绕行径合法但口径须标（paper轨/venue 账本分档不混）；③复审对「无源」声明的核验成本=一条 psql，必跑。
- **证据**：fin_cex_depth 52 万行龄 1min（SG 10-20 16:0xZ 实拍）；三票 STATE 行在卷（quoteFor/eval 五词/fee 补源）。
