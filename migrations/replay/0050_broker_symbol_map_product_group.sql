-- D-173 canonical 产品维度细化：broker_symbol_map 加 product_group 列。
-- 值域从 MT5 CalcMode 自动填充（fact.CalcModeToProductGroup）+ 人工补充；
-- NULL=未知，discoverSwapCombos 配对时放行（向后兼容）。
ALTER TABLE broker_symbol_map ADD COLUMN IF NOT EXISTS product_group text;
