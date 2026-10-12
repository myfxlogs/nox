-- 0144_carry_graduation_type_drop_and_flag_pair_cols.sql —— D-499（D-496 §8.6）：适配链尾件收口。
-- L1：carry_graduations.venue_type 惰性列拆除——0142 R2 统一加列（恒 'perp'），
--     D-498 拆配对双列后单 type 对 cfd 行（A 臂=MT）语义失真；Go/proto 零消费、
--     INSERT 不写（恒 default）。CHECK 约束 cg_venue_type_chk 随列级联删除。
ALTER TABLE carry_graduations DROP COLUMN venue_type;

-- L2：fin_venue_symbol_flags 配对域同法拆列（§8.5 同款药）——
-- venue（'裸名' 或 'a×b' 配对串）拆源列 venue_a/venue_b 直持裸域；
-- venue 退为 generated 配对投影（FlagKey 查找键形 'a|sym'/'a×b|sym' 零变化，
-- ListActiveFlags map 键侧不动）。
ALTER TABLE fin_venue_symbol_flags ADD COLUMN venue_a text NOT NULL DEFAULT '';
ALTER TABLE fin_venue_symbol_flags ADD COLUMN venue_b text NOT NULL DEFAULT '';
UPDATE fin_venue_symbol_flags SET
    venue_a = split_part(venue, '×', 1),
    venue_b = split_part(venue, '×', 2);
ALTER TABLE fin_venue_symbol_flags ALTER COLUMN venue_a DROP DEFAULT;
ALTER TABLE fin_venue_symbol_flags ALTER COLUMN venue_b DROP DEFAULT;
-- 活跃旗标部分索引锚 venue 列——先删再随列重建。
DROP INDEX fin_venue_symbol_flags_active_idx;
ALTER TABLE fin_venue_symbol_flags DROP COLUMN venue;
ALTER TABLE fin_venue_symbol_flags ADD COLUMN venue text
    GENERATED ALWAYS AS (CASE WHEN venue_b = '' THEN venue_a ELSE venue_a || '×' || venue_b END) STORED;
CREATE INDEX fin_venue_symbol_flags_active_idx
    ON fin_venue_symbol_flags (venue, symbol) WHERE resolved_at IS NULL;
ALTER TABLE fin_venue_symbol_flags ADD CONSTRAINT vsf_venue_a_bare_chk
    CHECK (venue_a <> '' AND venue_a NOT LIKE '%:%' AND venue_a NOT LIKE '%×%');
ALTER TABLE fin_venue_symbol_flags ADD CONSTRAINT vsf_venue_b_bare_chk
    CHECK (venue_b NOT LIKE '%:%' AND venue_b NOT LIKE '%×%');
COMMENT ON COLUMN fin_venue_symbol_flags.venue_a IS 'D-499 旗标源列·A 臂裸域（单 venue 旗=B 臂空）';
COMMENT ON COLUMN fin_venue_symbol_flags.venue_b IS 'D-499 旗标源列·B 臂裸域（空串=单 venue 旗）';
COMMENT ON COLUMN fin_venue_symbol_flags.venue IS 'D-499 展示性配对投影（generated：a 或 a×b，禁写）';
