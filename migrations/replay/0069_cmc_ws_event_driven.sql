-- D-225 CMC 事件驱动 v2.1：fin_cmc_prices 加 src 列 + fin_cmc_account_summary 加 alerts JSONB 列
-- 只加列（带 DEFAULT/可空），不改列不删列不回填历史行；REST 路径行为回退不依赖新列。

ALTER TABLE fin_cmc_prices ADD COLUMN IF NOT EXISTS src TEXT NOT NULL DEFAULT 'cmc_rest';
ALTER TABLE fin_cmc_account_summary ADD COLUMN IF NOT EXISTS alerts JSONB NOT NULL DEFAULT '[]';
