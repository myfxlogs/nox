-- 0160_paper_dual_cex_columns.sql —— D-555 S1：统一漏斗双CEX（FUNDING_HEDGE spot买×perp卖）纸面轨键域列。
-- 照 D-496 combo 拆分惯例：venue_a/venue_b 裸域两列已在（0142/0143），补 venue_type_a/b 源列
-- + venue_key_a/b 生成列（键域权威口径）。存量行 type=''→key=bare 零回填兼容；
-- psc_venue_bare_chk / cg_venue_*_bare_chk 不变（裸域列仍禁冒号）；双CEX 行 mt_venue=NULL 语义不变。

ALTER TABLE paper_spot_carry ADD COLUMN venue_type_a text NOT NULL DEFAULT '',
                                ADD COLUMN venue_type_b text NOT NULL DEFAULT '';
ALTER TABLE paper_spot_carry ADD COLUMN venue_key_a text
        GENERATED ALWAYS AS (CASE WHEN venue_type_a = '' THEN venue_a ELSE venue_a || ':' || venue_type_a END) STORED,
    ADD COLUMN venue_key_b text
        GENERATED ALWAYS AS (CASE WHEN venue_type_b = '' THEN venue_b ELSE venue_b || ':' || venue_type_b END) STORED;
COMMENT ON COLUMN paper_spot_carry.venue_type_a IS $$D-555 A 臂 venue_type（双CEX spot 腿=spot；mt×cex/存量行=空）$$;
COMMENT ON COLUMN paper_spot_carry.venue_type_b IS $$D-555 B 臂 venue_type（双CEX perp 腿=perp；mt×cex/存量行=空）$$;
COMMENT ON COLUMN paper_spot_carry.venue_key_a IS 'D-555 A 臂键域生成列（D-496 口径：type 空投影裸域）';
COMMENT ON COLUMN paper_spot_carry.venue_key_b IS 'D-555 B 臂键域生成列（D-496 口径：type 空投影裸域）';
ALTER TABLE paper_spot_carry ADD CONSTRAINT psc_venue_type_ab_chk
    CHECK (venue_type_a IN ('', 'spot', 'perp') AND venue_type_b IN ('', 'spot', 'perp'));

ALTER TABLE carry_graduations ADD COLUMN venue_type_a text NOT NULL DEFAULT '',
                              ADD COLUMN venue_type_b text NOT NULL DEFAULT '';
ALTER TABLE carry_graduations ADD COLUMN venue_key_a text
        GENERATED ALWAYS AS (CASE WHEN venue_type_a = '' THEN venue_a ELSE venue_a || ':' || venue_type_a END) STORED,
    ADD COLUMN venue_key_b text
        GENERATED ALWAYS AS (CASE WHEN venue_type_b = '' THEN venue_b ELSE venue_b || ':' || venue_type_b END) STORED;
COMMENT ON COLUMN carry_graduations.venue_type_a IS $$D-555 A 臂 venue_type（源 paper 行透传；存量行=空）$$;
COMMENT ON COLUMN carry_graduations.venue_type_b IS $$D-555 B 臂 venue_type（源 paper 行透传；存量行=空）$$;
COMMENT ON COLUMN carry_graduations.venue_key_a IS 'D-555 A 臂键域生成列（D-496 口径）';
COMMENT ON COLUMN carry_graduations.venue_key_b IS 'D-555 B 臂键域生成列（D-496 口径）';
ALTER TABLE carry_graduations ADD CONSTRAINT cg_venue_type_ab_chk
    CHECK (venue_type_a IN ('', 'spot', 'perp') AND venue_type_b IN ('', 'spot', 'perp'));
