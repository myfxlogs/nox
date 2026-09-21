-- 0116_xasset_combo_notional.sql - D-367：组合级 sizing 名义额列（业主令「.env 方案不恰当」→ sizing 收编 DB）。
-- 可空、零回填：NULL 行行为与今天完全一致（回退 ARB_OPP_NOTIONAL_USD 全局缺省，渐进收编无漂移）。
-- D-338 同表先例（xasset_allow_combos）；IF NOT EXISTS 幂等可重放（0114 先例）。
ALTER TABLE xasset_allow_combos ADD COLUMN IF NOT EXISTS notional_usd NUMERIC;
COMMENT ON COLUMN xasset_allow_combos.notional_usd IS 'D-367：组合级名义额（USD）；NULL=回退 ARB_OPP_NOTIONAL_USD 全局缺省';
