-- 0081_trx_account_tron_power.sql: D-211 返修——补 tron_power 列（质押量，sun 单位）
-- 根因：fin_trx_account.votes_total 存的是投票数（Σvote_count），dashboard 当质押量用。
-- 真实质押 TronPower = (bandwidthStaked + energyDelegated)，sun 单位，除以 1e6 得 TRX。
ALTER TABLE fin_trx_account ADD COLUMN IF NOT EXISTS tron_power BIGINT NOT NULL DEFAULT 0;
COMMENT ON COLUMN fin_trx_account.tron_power IS '质押量（sun，=bandwidthStaked+energyDelegated，除以 1e6 得 TRX）';
