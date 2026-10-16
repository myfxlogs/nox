-- 1040_paper_first_gate.sql —— 补洞②全模板纸面先行不变量（D-593，四次返修定值版）：
-- 真金执行须纸面证据或显式 D# 豁免；负偏结构（naked_short_vol）禁真金径。
ALTER TABLE fin_opp_template
    ADD COLUMN IF NOT EXISTS payoff_class text,
    ADD COLUMN IF NOT EXISTS paper_exempt_d text,
    ADD COLUMN IF NOT EXISTS paper_structures text[];
-- 九行显式映射（复审⑦⑧定值——禁 like 猜判据；defined_risk_vol=有限风险期权
-- 价差类如 option_spread；paper_structures=该模板证据域结构键集）。
ALTER TABLE fin_opp_template DROP CONSTRAINT IF EXISTS fin_opp_template_payoff_check;
UPDATE fin_opp_template t SET payoff_class=v.cls, paper_exempt_d=v.exempt,
    paper_structures=v.ps
    FROM (VALUES
      ('funding_carry','directional_hedged',NULL,ARRAY['perp_perp','perp_spot']),
      ('cfd_swap_carry','directional_hedged',NULL,ARRAY['cfd_swap_hedged']),
      ('earn_apr_lending','yield_on_holdings','D-286',ARRAY['earn_lending']),
      ('dci_buy_low','naked_short_vol',NULL,ARRAY[]::text[]),
      ('dci_sell_high_covered_call','yield_on_holdings','D-593',ARRAY['dci_covered_call']),
      ('option_spread','defined_risk_vol',NULL,ARRAY['option_spread']),
      ('cross_venue_spot','directional_hedged',NULL,ARRAY['cross_venue']),
      ('term_basis','directional_hedged',NULL,ARRAY['term_basis']),
      ('cash_mgmt','cash_mgmt','D-286',ARRAY['cash_mgmt'])
    ) AS v(id,cls,exempt,ps)
    WHERE t.id=v.id;
UPDATE fin_opp_template SET payoff_class='other' WHERE payoff_class IS NULL;
ALTER TABLE fin_opp_template ALTER COLUMN payoff_class SET NOT NULL;
ALTER TABLE fin_opp_template ADD CONSTRAINT fin_opp_template_payoff_check
    CHECK (payoff_class IN
      ('directional_hedged','naked_short_vol','yield_on_holdings','defined_risk_vol','cash_mgmt','other'));
