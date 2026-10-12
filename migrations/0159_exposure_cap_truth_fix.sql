-- 0159 D-547 S2：敞口与组合金额真相修正（干跑留痕见 LOG.md S0④）。
-- ①组合 max_gross_usd 对齐「单腿名义×2×1.05」——修复结构自锁（2×notional>gross 永不可批；
--   cap 只修仍自锁的行，值仍超池的组合属提案表业主拍板域，本迁移不代拍）。
-- ②幽灵键重键名 okx:unified→okx:perp——闸键域须匹配 exchange_configs 键域
--   （okx 无 unified 行，real 组合腿全走 okx:perp；unified 行保护不了任何真腿）。
-- 均 WHERE 守卫幂等（二跑零命中）；ON CONFLICT 补闸防 okx:perp 已有人工行撞 PK。
UPDATE xasset_allow_combos
SET max_gross_usd = CEIL(notional_usd * 2 * 1.05 / 100) * 100,
    note = CASE WHEN note LIKE '%D-547%' THEN note ELSE note || ' D-547 帽对齐 2×notional' END
WHERE (simulated IS NULL OR simulated = false)
  AND notional_usd IS NOT NULL
  AND 2 * notional_usd > max_gross_usd;

INSERT INTO fin_venue_exposure_caps (venue_key, cap_usd, enabled, note)
SELECT 'okx:perp', cap_usd, enabled, note || ' →D-547 重键名'
FROM fin_venue_exposure_caps
WHERE venue_key = 'okx:unified'
ON CONFLICT (venue_key) DO NOTHING;

DELETE FROM fin_venue_exposure_caps
WHERE venue_key = 'okx:unified'
  AND EXISTS (SELECT 1 FROM fin_venue_exposure_caps WHERE venue_key = 'okx:perp');
