-- D-338 S1：XASSET 白名单表化（env=类目错置勘正；两钥制=表行+既有 ARB_LIVE_TRADE，零新 env）。
-- 播种四行=当前 .env:27 逐字对齐（施工前已实读复核）；simulated 对齐 exchange_configs 习惯：
-- true=仅 demo / false=仅 real / NULL=两域通用；enabled=false=全拒；note=D#/业主确认标记位。
CREATE TABLE IF NOT EXISTS xasset_allow_combos (
    id             BIGSERIAL PRIMARY KEY,
    canonical_buy  TEXT NOT NULL,
    canonical_sell TEXT NOT NULL,
    venue_buy      TEXT NOT NULL,
    venue_sell     TEXT NOT NULL,
    max_long_qty   NUMERIC NOT NULL CHECK (max_long_qty > 0),
    max_short_qty  NUMERIC NOT NULL CHECK (max_short_qty > 0),
    max_gross_usd  NUMERIC NOT NULL CHECK (max_gross_usd > 0),
    enabled        BOOLEAN NOT NULL DEFAULT true,
    simulated      BOOLEAN,
    note           TEXT NOT NULL DEFAULT '',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (canonical_buy, canonical_sell, venue_buy, venue_sell)
);
INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_sell, max_long_qty, max_short_qty, max_gross_usd, simulated, note) VALUES
 ('CLUSDT','WTI','gate:perp','277842155:mt5',2000,0.05,2500,true,'D-330 demo seed'),
 ('XAUUSD','XAUUSDT','277842155:mt5','gate:perp',0.01,10000,10000,true,'D-330 demo seed'),
 ('XAUUSD','XAUUSDT','277842155:mt5','binance:perp',0.01,5,10000,true,'D-330 demo seed'),
 ('XAUUSD','PAXGUSDT','277842155:mt5','binance:perp',0.01,5,10000,true,'D-330 demo seed')
ON CONFLICT DO NOTHING;
