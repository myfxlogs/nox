-- 1040_paper_first_gate.sql —— 补洞②全模板纸面先行不变量（D-593）：
-- 真金执行须纸面证据或显式 D# 豁免；负偏结构（naked_short_vol）禁真金径。
ALTER TABLE fin_opp_template
    ADD COLUMN IF NOT EXISTS payoff_class text,
    ADD COLUMN IF NOT EXISTS paper_exempt_d text;
ALTER TABLE fin_opp_template DROP CONSTRAINT IF EXISTS fin_opp_template_payoff_check;
ALTER TABLE fin_opp_template ADD CONSTRAINT fin_opp_template_payoff_check
    CHECK (payoff_class IS NULL OR payoff_class IN
      ('directional_hedged','naked_short_vol','yield_on_holdings','cash_mgmt','other'));
-- 已知负偏结构如实标（dci_buy_low=卖 put 负偏，SG 4 结算单实证净亏）。
UPDATE fin_opp_template SET payoff_class='naked_short_vol' WHERE id LIKE 'dci%';
UPDATE fin_opp_template SET payoff_class='yield_on_holdings'
    WHERE payoff_class IS NULL AND (id LIKE 'earn%' OR id LIKE 'cash%');
UPDATE fin_opp_template SET payoff_class='directional_hedged'
    WHERE payoff_class IS NULL AND id NOT LIKE 'dci%';
