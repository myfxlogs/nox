# 代理腿配对=相关性赌注非收敛套利（2026-10-16 · D-583）

**情境**：armed 白名单 PAXGUSDT×XAUUSD（#16）、CLUSDT×WTI（#1）——不同品种身份按「同资产代理」配对冲。
**实数**：PAXG funding −2.3%+IC swap_short +0.9 账面息差薄（~3%/yr），代价=PAXG 代币脱锚尾风险；kb verified 同构坑=arb CFD 跨所同标的 71 笔全亏 −$83.95。
**根因**：收敛承诺要求同一产品身份（同合约月/定价源）；代理关系只是相关性，相关性断裂时无收敛兜底。
**判据挂接**：EA-08（代理腿 armed 结构性拒）+EA-07（同标的真伪）。
**回改点**：combo_gate:underlying-consistency（落册前三源互证——本票机械直译闸，#20 同型防复）
match:
  canonicals: [XAUUSD, PAXGUSDT, CLUSDT, WTI]
**证据**：xasset_allow_combos #16/#1 行+kb `cfd-cross-venue-structural-spread`。
**处置**：#16/#1 退役（enabled=false）。
