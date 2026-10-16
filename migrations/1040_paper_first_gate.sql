-- 1040_paper_first_gate.sql —— 补洞②全模板纸面先行不变量（D-593）：
-- 真金执行须纸面证据或显式 D# 豁免；负偏结构（naked_short_vol）禁真金径。
ALTER TABLE fin_opp_template
    ADD COLUMN IF NOT EXISTS payoff_class text,
    ADD COLUMN IF NOT EXISTS paper_exempt_d text;
ALTER TABLE fin_opp_template DROP CONSTRAINT IF EXISTS fin_opp_template_payoff_check;
ALTER TABLE fin_opp_template ADD CONSTRAINT fin_opp_template_payoff_check
    CHECK (payoff_class IS NULL OR payoff_class IN
      ('directional_hedged','naked_short_vol','yield_on_holdings','cash_mgmt','other'));
-- 九行显式映射（复审⑦定值——禁 like 猜判据；NULL=未分类绕负偏闸故全钉）：
UPDATE fin_opp_template SET payoff_class=v.cls, paper_exempt_d=v.exempt
    FROM (VALUES
      ('funding_carry','directional_hedged',NULL),
      ('cfd_swap_carry','directional_hedged',NULL),
      ('earn_apr_lending','yield_on_holdings','D-286'),   -- 承接位无方向敞口豁免
      ('dci_buy_low','naked_short_vol',NULL),            -- 负偏卖 put——结构性禁真金
      ('dci_sell_high_covered_call','yield_on_holdings','D-593'), -- 托管 carry 主线豁免
      ('option_spread','directional_hedged',NULL),
      ('cross_venue_spot','directional_hedged',NULL),
      ('term_basis','directional_hedged',NULL),
      ('cash_mgmt','cash_mgmt','D-286')                  -- 现金管理承接位豁免
    ) AS v(id,cls,exempt)
    WHERE fin_opp_template.id=v.id;
