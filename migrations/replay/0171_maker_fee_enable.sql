-- 0171_maker_fee_enable.sql D-018 后段：exchange_configs.maker_fee_bps——maker 腿
-- econ 费率源（venue 官方费率页 VIP0 事实，业主可改）；NULL=未配置→econ 回落 taker 保守口径。
-- 播种仅填 NULL 位不覆盖业主热改；gate/mexc perp maker 未实测 → 留 NULL（回落 taker）。
-- 同迁 app_config 写入 maker 启用键（D-018 后段③：不改 .env；app_config 行 > env > default）；
-- ON CONFLICT DO NOTHING 保留既有热改行。
ALTER TABLE exchange_configs ADD COLUMN IF NOT EXISTS maker_fee_bps DOUBLE PRECISION;
UPDATE exchange_configs SET maker_fee_bps = v.fee
FROM (VALUES
  ('binance','spot',10.0),('binance','perp',2.0),
  ('okx','spot',8.0),('okx','perp',2.0)
) AS v(venue, venue_type, fee)
WHERE exchange_configs.venue = v.venue
  AND exchange_configs.venue_type = v.venue_type
  AND exchange_configs.maker_fee_bps IS NULL;
INSERT INTO app_config (key, value, note) VALUES
  ('ARB_MAKER_TIMEOUT','20s','D-018 maker：首腿 post-only 挂单超时 T，到点撤单'),
  ('ARB_MAKER_MAX_PER_HOUR','3','D-018 maker：每 opp 每小时首腿挂单 attempt 上限 N')
ON CONFLICT (key) DO NOTHING;
