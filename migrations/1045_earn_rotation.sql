-- 1045_earn_rotation.sql —— 息差轮动引擎模板注册（D-612 业主令/D-613 核验照准）：
-- earn_rotation=活期仓间轮动（同资产低效仓赎回转最优净 APR 档）入 autoexec 豁免族
-- （payoff=yield_on_holdings，豁免引用=D-612——所内赎转属承接位常设授权）；
-- fin_strategy 直接落 live（轮动判据+守卫全落表，晋级即授权同 cash_mgmt 先例）。
INSERT INTO fin_opp_template (id, mechanism, net_apr, exec_track, risk_level,
    payoff_class, paper_exempt_d, paper_structures, autoexec) VALUES
 ('earn_rotation','同资产活期仓间轮动（低效仓赎回转最优净 APR 档）',
  '目标APR−现仓APR−申赎双程费−赎回时滞机会成本（分级档）','auto','low',
  'yield_on_holdings','D-612',ARRAY['earn_lending'],true)
ON CONFLICT (id) DO UPDATE SET
  payoff_class='yield_on_holdings', paper_exempt_d='D-612',
  paper_structures=ARRAY['earn_lending'], autoexec=true;
INSERT INTO fin_opp_template_cond (template_id, seq, kind, subject, qualifier, min_val) VALUES
 ('earn_rotation',1,'permission','earn_subscribe+earn_redeem','venue_api',NULL),
 ('earn_rotation',2,'capital','USDT','min_usd',100)
ON CONFLICT (template_id, seq) DO NOTHING;
INSERT INTO fin_strategy (strategy, state, decided_by)
VALUES ('earn_rotation','live','D-612')
ON CONFLICT (strategy) DO NOTHING;
