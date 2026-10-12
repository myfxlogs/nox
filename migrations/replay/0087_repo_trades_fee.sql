-- D-276 逆回购手续费模型（D-272 增量）。
-- fee_cny = 成交金额 × 品种佣金率（一次性，非年化、非按占用天数）。
-- 费率来源：204001=生产实证 9-11（407,000×0.00001=4.07）；其余三品种按上交所公布回购佣金率，待首个实测成交日后校准。

ALTER TABLE cnfi_repo_trades ADD COLUMN IF NOT EXISTS fee_cny DOUBLE PRECISION;

-- 存量回填（费率按品种 CASE）
UPDATE cnfi_repo_trades SET fee_cny = principal_cny * CASE instrument_code
    WHEN '204001' THEN 0.00001
    WHEN '204007' THEN 0.00005
    WHEN '204014' THEN 0.00010
    WHEN '204028' THEN 0.00020
END
WHERE fee_cny IS NULL;
