-- 1041_template_autoexec.sql —— D-598：承接位提案自动执行判据列。
-- autoexec=true 模板提案落库即直进 GoProposal（豁免族=委员会级常设授权
-- 语义——提案只是记账/透出载体）；禁非豁免族 true（CHECK 钉死与
-- paper_exempt_d 联动——非豁免=须纸面证据或 D# 豁免才授权）。
ALTER TABLE fin_opp_template ADD COLUMN IF NOT EXISTS autoexec boolean DEFAULT false NOT NULL;
ALTER TABLE fin_opp_template DROP CONSTRAINT IF EXISTS fin_opp_template_autoexec_check;
ALTER TABLE fin_opp_template ADD CONSTRAINT fin_opp_template_autoexec_check
    CHECK (NOT autoexec OR paper_exempt_d IS NOT NULL);
UPDATE fin_opp_template SET autoexec=true WHERE id IN ('earn_apr_lending','cash_mgmt');
