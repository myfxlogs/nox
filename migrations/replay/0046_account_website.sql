-- D-157 V3：账号/交易所官网列（业主自助管理；空 = 未设置）
-- IF NOT EXISTS：生产 DB 可能已手动加过该列（D-157 V3 部署时），重复 ADD 报 42701。
ALTER TABLE broker_accounts ADD COLUMN IF NOT EXISTS website TEXT NOT NULL DEFAULT '';
ALTER TABLE exchange_configs ADD COLUMN IF NOT EXISTS website TEXT NOT NULL DEFAULT '';
