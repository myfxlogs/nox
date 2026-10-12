-- 0143_carry_graduation_pair_cols.sql —— D-498（D-496 §8.5）：carry_graduations ×配对串域整改。
-- venue（'a×b' 配对串）拆源列 venue_a/venue_b 直持裸域；venue 退为展示性配对投影
-- （venue_key 生成列同构：源列+展示投影分层，库保证单向——消费端直读双列断解析）。
ALTER TABLE carry_graduations ADD COLUMN venue_a text NOT NULL DEFAULT '';
ALTER TABLE carry_graduations ADD COLUMN venue_b text NOT NULL DEFAULT '';
UPDATE carry_graduations SET
    venue_a = split_part(venue, '×', 1),
    venue_b = split_part(venue, '×', 2);
ALTER TABLE carry_graduations ALTER COLUMN venue_a DROP DEFAULT;
ALTER TABLE carry_graduations ALTER COLUMN venue_b DROP DEFAULT;
-- venue 列转 generated 展示投影（旧列 DROP 重加；原 cg_venue_bare_chk 随列删除，
-- 裸域约束改锚 venue_a/venue_b 两源列——'×' 分隔符本身亦禁入源列）。
ALTER TABLE carry_graduations DROP COLUMN venue;
ALTER TABLE carry_graduations ADD COLUMN venue text
    GENERATED ALWAYS AS (CASE WHEN venue_b = '' THEN venue_a ELSE venue_a || '×' || venue_b END) STORED;
ALTER TABLE carry_graduations ADD CONSTRAINT cg_venue_a_bare_chk
    CHECK (venue_a <> '' AND venue_a NOT LIKE '%:%' AND venue_a NOT LIKE '%×%');
ALTER TABLE carry_graduations ADD CONSTRAINT cg_venue_b_bare_chk
    CHECK (venue_b NOT LIKE '%:%' AND venue_b NOT LIKE '%×%');
COMMENT ON COLUMN carry_graduations.venue_a IS 'D-498 配对源列·A 臂裸域（cfd=MT 账号号；spot_carry=CEX 名）';
COMMENT ON COLUMN carry_graduations.venue_b IS 'D-498 配对源列·B 臂裸域（cfd=CEX 名；单 venue 行=空串）';
COMMENT ON COLUMN carry_graduations.venue IS 'D-498 展示性配对投影（generated：a 或 a×b，禁写）';
