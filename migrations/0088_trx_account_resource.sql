-- 0088_trx_account_resource.sql: D-278 S5——地址资源显示（剩余能量/带宽值）。
-- 根因：account_collector 已调 getaccountresource 但只存 net_limit 上限，使用量与能量维度全缺。
-- 新增 4 列：net_used/free_net_used/energy_limit/energy_used（sun 单位，÷1e6 得 TRX）。
-- 与 D-269 预检联动：剩余带宽 <500 黄/<250 红（阈值=trxvote 常量同源引用禁双写）。

ALTER TABLE fin_trx_account ADD COLUMN IF NOT EXISTS net_used BIGINT NOT NULL DEFAULT 0;
ALTER TABLE fin_trx_account ADD COLUMN IF NOT EXISTS free_net_used BIGINT NOT NULL DEFAULT 0;
ALTER TABLE fin_trx_account ADD COLUMN IF NOT EXISTS free_net_limit BIGINT NOT NULL DEFAULT 0;
ALTER TABLE fin_trx_account ADD COLUMN IF NOT EXISTS energy_limit BIGINT NOT NULL DEFAULT 0;
ALTER TABLE fin_trx_account ADD COLUMN IF NOT EXISTS energy_used BIGINT NOT NULL DEFAULT 0;

COMMENT ON COLUMN fin_trx_account.net_used IS '已用带宽（sun，NetUsed，÷1e6 得 TRX）';
COMMENT ON COLUMN fin_trx_account.free_net_used IS '免费已用带宽（sun，FreeNetUsed）';
COMMENT ON COLUMN fin_trx_account.free_net_limit IS '免费带宽上限（sun，FreeNetLimit）';
COMMENT ON COLUMN fin_trx_account.energy_limit IS '能量上限（sun，EnergyLimit，÷1e6 得 TRX）';
COMMENT ON COLUMN fin_trx_account.energy_used IS '已用能量（sun，EnergyUsed，÷1e6 得 TRX）';
