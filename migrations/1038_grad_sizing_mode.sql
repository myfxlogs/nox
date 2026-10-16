-- D-581 毕业批准拆层：sizing_mode 三态（fixed=四字段固化 / pct=pct 原样落表
-- armed 径活池折算 / system=零 sizing 输入默认档 app_config 包络）。
-- 非 fixed 档腿帽留 NULL（写表值=pct/模式，禁 armed 态二次折算写死值）；
-- fixed 档维持 >0 硬约束（模式内移判据进 CHECK 不变弱）。
ALTER TABLE xasset_allow_combos
    ALTER COLUMN max_long_qty DROP NOT NULL,
    ALTER COLUMN max_short_qty DROP NOT NULL,
    ALTER COLUMN max_gross_usd DROP NOT NULL,
    ADD COLUMN IF NOT EXISTS sizing_mode text NOT NULL DEFAULT 'fixed',
    ADD COLUMN IF NOT EXISTS funding_pct numeric;

ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_allow_combos_max_long_qty_check;
ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_allow_combos_max_short_qty_check;
ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_allow_combos_max_gross_usd_check;
ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_allow_combos_max_long_qty_check
    CHECK (max_long_qty IS NULL OR max_long_qty > 0);
ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_allow_combos_max_short_qty_check
    CHECK (max_short_qty IS NULL OR max_short_qty > 0);
ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_allow_combos_max_gross_usd_check
    CHECK (max_gross_usd IS NULL OR max_gross_usd > 0);

ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_allow_combos_sizing_mode_check;
ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_allow_combos_sizing_mode_check
    CHECK (sizing_mode IN ('fixed','pct','system'));
ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_allow_combos_sizing_pct_check;
ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_allow_combos_sizing_pct_check
    CHECK (sizing_mode <> 'pct' OR funding_pct > 0);
ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_allow_combos_sizing_caps_check;
ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_allow_combos_sizing_caps_check
    CHECK (sizing_mode = 'fixed'
           OR (max_long_qty IS NULL AND max_short_qty IS NULL
               AND max_gross_usd IS NULL AND notional_usd IS NULL));
