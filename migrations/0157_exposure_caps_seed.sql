-- 0157 D-543：fin_venue_exposure_caps 种子五行（表前全空=「未设敞口闸」warn 全 venue 生效）。
-- 值=9-28 各池实测取整（cap 语义=毛敞口上限，种子值≤池值不凭空放大可开敞口）；
-- ON CONFLICT DO NOTHING 幂等（已有人工行不被覆盖）。动态 cap（随池实时值）另议不混票。
INSERT INTO fin_venue_exposure_caps (venue_key, cap_usd, enabled, note) VALUES
    ('binance:spot',    20000, true, 'D-543 种子=9-28 池值 20262 取整'),
    ('binance:perp',     1300, true, 'D-543 种子=9-28 池值 1345 取整'),
    ('binance:funding',  1000, true, 'D-543 种子=9-28 池值 1000 取整'),
    ('gate:perp',         100, true, 'D-543 种子=9-28 池值 100（gate 池现值极小，充值后业主上调）'),
    ('okx:unified',      1500, true, 'D-543 种子=9-28 池值 1525 取整')
ON CONFLICT (venue_key) DO NOTHING;
