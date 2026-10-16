# DCI 卖 put 负偏结构（2026-10-16 · D-584）

**情境**：binance 双币「低接币」PUT，1-2 天期，APR 报 9.7-20.2%。
**实数**：4 结算单 #7/8/10 收利息 +1.41/+1.40/+1.14；#11 strike 83,000 行权时市价 81,300 → 358 USDT 变 0.00433 BTC，−$5.6~7.3。**3 赚不够 1 亏**；残余 BTC 裸敞口。
**根因**：卖 put=权利金封顶/下行不封顶负偏结构——收益是承担下行风险的风险补偿，非价差/息差。违 D-019。
**判据挂接**：EA-05（naked_short_vol 结构拒真金）+EA-18（行权残余裸敞口须处置径）。
match:
  payoff_class: naked_short_vol
**证据**：SG fin_dci_order #7-11 行+facts BTCUSDT index_price 81,300@10-15 08:12Z。
**处置**：dci_buy_low 停板（fin_opp_template.enabled=f）；残余 BTC 出清径在办。
