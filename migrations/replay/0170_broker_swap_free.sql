-- 0170_broker_swap_free.sql D-014 S1：broker_accounts.swap_free——账号免 swap 旗标
-- + exchange_configs.taker_fee_bps——CEX 腿 econ 费率源（venue 官方费率页事实，业主可改）。
-- 3001157（venue=登录账号号）免 swap 账户：置 true；旗=true 则 econ 口径 swap=0/src=account_flag 不回退 spec。
-- taker_fee_bps NULL=未配置→econ/paper 皆 fail-closed（禁默认值）。
-- 播种值=D-474a 钉稿 venue 官方费率页（binance spot 10/perp 4.5；okx 10/5；gate 10/5；mexc 5/2），
-- 仅填 NULL 位不覆盖业主热改。
ALTER TABLE broker_accounts ADD COLUMN IF NOT EXISTS swap_free BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE exchange_configs ADD COLUMN IF NOT EXISTS taker_fee_bps DOUBLE PRECISION;
UPDATE broker_accounts SET swap_free = true WHERE venue = '3001157';
UPDATE exchange_configs SET taker_fee_bps = v.fee
FROM (VALUES
  ('binance','spot',10.0),('binance','perp',4.5),('binance','funding',5.0),
  ('okx','spot',10.0),('okx','perp',5.0),('okx','funding',5.0),
  ('gate','spot',10.0),('gate','perp',5.0),('gate','funding',5.0),
  ('mexc','spot',5.0),('mexc','perp',2.0),('mexc','funding',2.0)
) AS v(venue, venue_type, fee)
WHERE exchange_configs.venue = v.venue
  AND exchange_configs.venue_type = v.venue_type
  AND exchange_configs.taker_fee_bps IS NULL;
