-- D-208 v2：personal_positions 加 cmc_instrument_id 列（CMC 行数据源切换标识）
-- 复审方执行：UPDATE personal_positions SET cmc_instrument_id='X-AFQUL' WHERE asset='TRX' AND kind='short_hedge';

ALTER TABLE personal_positions ADD COLUMN IF NOT EXISTS cmc_instrument_id VARCHAR(32);
