# paper 沙盒入口闸口径审计（D-663 族续件·纯取证零改码）

立件：paper cex carry `opened=0 skips=depth_low:10/depth_miss:…` 常态行→
eval 卡四字段同缺→毕业链零推进（「没过观察期」诊断续件）。

## ①入口闸对拍：paper vs 实盘

| 闸 | paper（paper_cex_carry.go:107-145） | 实盘 | 纸面意义裁定 |
|---|---|---|---|
| blocked flag | 同款消费 | 同款 | 保（执行语义直通闸不拆） |
| depth_missing/low | 双腿 24h `fin_cex_depth` mid_depth_usd 均值≥MinDepthUSD，**无样本 fail-closed** | 实盘无此闸（depth=滑点估计输入非拦件） | **病灶主件**——采集面=执行腿才写样本（fin_cex_depth 采集于 deep 簿订阅=armed 域），非执行面 symbol n=0→depth_missing 恒拒=全谱纸面学不了 |
| funding_low | funding_history 7d 均≥MinFundingPct | 实盘同参 eval | 保（收益判据纸面=实盘同口径才对版） |
| quote_dead/cooldown | 同款 | 同款 | 保 |

**对拍结论**：depth 闸本意=滑点证据质量闸，但它把「采集覆盖缺」翻译成了「机会不合格」——
纸面候选的宇宙集天然超深度采集面（采集=执行腿订阅集），结构性
n=0→fail-closed 全拒。**纸面无意义处**：滑点在纸面本按簿中价记，
depth 深度数据未采≠市况无深度——该闸对纸面语义为「要求额外采集」
非「筛选真候选」。

## ②cost 链四字段缺因定性

`fin_opp_eval` fee/slippage/failure/window——逐字段读面定位（oppmatch/eval.go）：

| 字段 | 产制面 | 缺因 |
|---|---|---|
| fee | 模板机制如实填（earn/申购族 trackPrior「不适用」）或 paper 往返费算（paper_fees.go——paperSpotCarryFeeUSD 四笔；费率缺→nil 拒填） | **写入条件缺**——paper 没开=无 fee 可算（opened=0 上游断链） |
| slippage | `fin_cex_depth` 中位半价差（trackLive）/cexd 半价差 | **采集缺**——depth 样本面=执行腿集，候选符号无行→字段 nil |
| failure | `fin_dci_order`/`fin_venue_reject` 败数（trackReplay） | **写入缺（下游）**——零开=零单=零败样本 |
| window | `fin_cex_earn_apr` 30d 中位/纸面窗口回放 | 部分模板产制缺，但主因=paper 零实绩链断 |

**定性**：四字段同缺非四处独立病——主因=depth 闸把入口掐死（
opened=0→fee/failure/window 无产出材料）+slippage 副因=depth 采集
面窄（同样本面病灶）。修源=闸口径修+采集面扩二件，字段自愈。

## ③paper 闸口径建议稿（复审面过设计验证再动码）

- **paper depth 闸改「三层降级」**：有样本仍按 MinDepthUSD 判（口径
  与实盘同——有证据就要证据够）；无样本不设 fail-closed 硬拒，改落
  `depth_unproven` 提案档入 paper（纸面职责=在成本不确定下照样模拟，
  沙盒落成后 eval 凭实绩判）；实盘闸零动（禁线）。
- **depth 采集面扩**：`fin_cex_depth` 写样本域从执行腿集扩到 paper
  候选集（REST 拍顺带取顶档一行——载荷轻）——滑点字段一并复供，
  「采集缺」副因同修。
- **毕业判据口径成文**（纸面域）：paper→armed 毕业须双证=纸面净
  累计为正+成本链四字段非缺（evaluable）——`depth_unproven` 档
  paper 可开但毕业须等 depth 证据补采或标注「成本未证」降级口径。
- **不动一分一毫**：实盘执行闸、MinFundingPct/flag/quote_dead 语义。

## 禁线核验

纯取证件——零改码/零闸语义动；实盘闸件仅对拍未触。
