-- D-208 v2-A：fin_cmc_positions 加 opened_at + order_id 列（AccountCollector 解析 orders[0]）

ALTER TABLE fin_cmc_positions ADD COLUMN IF NOT EXISTS opened_at TIMESTAMPTZ;
ALTER TABLE fin_cmc_positions ADD COLUMN IF NOT EXISTS order_id VARCHAR(64);
