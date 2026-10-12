-- 0188_opp_template.sql D-185：机会模板册（D-184 总纲衍生①）——可枚举收益机会
-- 全编目机器可读：模板={收益机制, 结构化条件清单(子表逐条可评估,D-187 消费), 净APR口径,
-- 执行轨(auto/semi/proposal), 风险级}。条件 kind 枚举=票面钦定五值。
CREATE TABLE IF NOT EXISTS fin_opp_template (
  id         text PRIMARY KEY,
  mechanism  text NOT NULL,           -- 收益机制一句话
  net_apr    text NOT NULL,           -- 净 APR 算法口径（毛−摩擦−失败成本）
  exec_track text NOT NULL DEFAULT 'proposal'
             CHECK (exec_track IN ('auto','semi','proposal')),
  risk_level text NOT NULL DEFAULT 'mid' CHECK (risk_level IN ('low','mid','high')),
  enabled    boolean NOT NULL DEFAULT true,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS fin_opp_template_cond (
  template_id text NOT NULL REFERENCES fin_opp_template(id) ON DELETE CASCADE,
  seq         int  NOT NULL,
  kind        text NOT NULL CHECK (kind IN ('credential','capital','permission','constraint','holding')),
  subject     text NOT NULL,          -- credential=venue / capital=ccy / permission=endpoint / constraint=field / holding=asset
  qualifier   text NOT NULL DEFAULT '', -- credential=cap / capital=min_usd键 / constraint=限值语义
  min_val     double precision,       -- 数值条件（min_usd/min_qty/份额上限）
  PRIMARY KEY (template_id, seq)
);

-- 首批 ≥8 族（模板变更走 D# 留痕）。
INSERT INTO fin_opp_template (id, mechanism, net_apr, exec_track, risk_level) VALUES
 ('funding_carry','CEX perp funding 收取（空合约多现货/反向）','funding_rate×365 − taker×换手 − 撤单失败成本','auto','mid'),
 ('cfd_swap_carry','CFD swap 息差（多单负 swap 空对冲）','swap_daily×365 − 点差成本 − 展期滑点','auto','mid'),
 ('earn_apr_lending','所内理财活期/定期 APR（USDT 闲置生息）','apy − 赎回摩擦 − 平台风险溢价','semi','low'),
 ('dci_buy_low','DCI/双币卖看跌低价接币（put-selling 贴现）','apy − 行权接币不利价差 − 机会成本','proposal','mid'),
 ('dci_sell_high_covered_call','持仓币卖 DCI 高价=covered call（托管 carry 主线）','apy − 行权踏空风险 − 份额约束摩擦','proposal','mid'),
 ('option_spread','期权利差（买低卖高/日历价差）','权利金净差 − 行权成本 − 流动性滑点','proposal','high'),
 ('cross_venue_spot','现货跨所价差收敛','spread_bps − 双边 taker − 提币成本','auto','mid'),
 ('term_basis','期限基差收敛（现货×远月期）','annualized_basis − 双边费 − funding 对冲成本','proposal','mid'),
 ('cash_mgmt','闲置稳定币现金管理（理财/活期归集）','apy − 归集摩擦','semi','low')
ON CONFLICT (id) DO NOTHING;

INSERT INTO fin_opp_template_cond (template_id, seq, kind, subject, qualifier, min_val) VALUES
 ('funding_carry',1,'credential','okx:perp','private_read+trade',NULL),
 ('funding_carry',2,'credential','gate:perp','private_read+trade',NULL),
 ('funding_carry',3,'capital','USDT','min_usd',3000),
 ('funding_carry',4,'constraint','cex_exposure_pct','max',35),
 ('cfd_swap_carry',1,'credential','mt5','private_read+trade',NULL),
 ('cfd_swap_carry',2,'constraint','cfd_exposure_pct','max',25),
 ('cfd_swap_carry',3,'constraint','gold_oz_90d','max',3),
 ('earn_apr_lending',1,'credential','okx:spot','private_read',NULL),
 ('earn_apr_lending',2,'credential','binance:spot','private_read',NULL),
 ('earn_apr_lending',3,'permission','earn_subscribe','venue_api',NULL),
 ('earn_apr_lending',4,'capital','USDT','min_usd',500),
 ('dci_buy_low',1,'credential','gate:dual','dci_order',NULL),
 ('dci_buy_low',2,'permission','dual_order','venue_api',NULL),
 ('dci_buy_low',3,'capital','USDT','min_usd',1000),
 ('dci_sell_high_covered_call',1,'holding','BTC','min_qty',0.01),
 ('dci_sell_high_covered_call',2,'permission','dual_order','venue_api',NULL),
 ('dci_sell_high_covered_call',3,'constraint','custody_share','max',0.34),
 ('dci_sell_high_covered_call',4,'constraint','custody_redeemable','always',NULL),
 ('option_spread',1,'credential','okx:option','private_read+trade',NULL),
 ('option_spread',2,'capital','USDT','min_usd',2000),
 ('cross_venue_spot',1,'credential','okx:spot','private_read+trade',NULL),
 ('cross_venue_spot',2,'credential','binance:spot','private_read+trade',NULL),
 ('cross_venue_spot',3,'capital','USDT','min_usd',2000),
 ('term_basis',1,'credential','okx:futures','private_read+trade',NULL),
 ('term_basis',2,'capital','USDT','min_usd',3000),
 ('term_basis',3,'constraint','cex_exposure_pct','max',35),
 ('cash_mgmt',1,'permission','earn_subscribe','venue_api',NULL),
 ('cash_mgmt',2,'capital','USDT','min_usd',100)
ON CONFLICT (template_id, seq) DO NOTHING;
