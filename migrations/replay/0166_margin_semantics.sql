-- 0166 D-010：保证金口径重构。
-- ① xasset_allow_combos 增 margin_usd=组合保证金总预算（双腿占用和上限）；
--    与 notional_usd 互斥（同预算双口径歧义禁并存），CHECK 层钉死。
-- ② fin_venue_exposure_caps.cap_usd 语义翻=venue 保证金占用预算（列名不动，
--    note 记语义翻转日；种子数值沿用——9-28 池值取整在新语义下仍保守，调值属业主运维域）。
ALTER TABLE xasset_allow_combos ADD COLUMN IF NOT EXISTS margin_usd NUMERIC;
COMMENT ON COLUMN xasset_allow_combos.margin_usd IS 'D-010：组合保证金总预算 USD（双腿占用和上限）；NULL=未设；与 notional_usd 互斥';
ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_allow_combos_margin_pos;
ALTER TABLE xasset_allow_combos
    ADD CONSTRAINT xasset_allow_combos_margin_pos CHECK (margin_usd IS NULL OR margin_usd > 0);
ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_allow_combos_budget_mutex;
ALTER TABLE xasset_allow_combos
    ADD CONSTRAINT xasset_allow_combos_budget_mutex
    CHECK (NOT (notional_usd IS NOT NULL AND margin_usd IS NOT NULL));

UPDATE fin_venue_exposure_caps
SET note = note || ' D-010 cap 语义翻=保证金占用预算(2026-10-01)',
    updated_at = now()
WHERE note NOT LIKE '%D-010%';
