-- D-157 V3：账号/交易所官网列（业主自助管理；空 = 未设置）
ALTER TABLE broker_accounts ADD COLUMN website TEXT NOT NULL DEFAULT '';
ALTER TABLE exchange_configs ADD COLUMN website TEXT NOT NULL DEFAULT '';
