-- D-271 CEX 只读扫描器：理财 APR + 跨所价差 + exchange_configs 主网只读行。
-- 凭据列留空——业主通过 DB UPDATE 填入（不入 git）。

CREATE TABLE IF NOT EXISTS fin_cex_earn_apr (
    id          BIGSERIAL PRIMARY KEY,
    venue       VARCHAR(16) NOT NULL,
    ccy         VARCHAR(16) NOT NULL,
    product_type VARCHAR(16) NOT NULL,
    apr         NUMERIC(10,6) NOT NULL,
    tier_apr    TEXT,
    ts          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_cex_earn_apr_venue_ccy ON fin_cex_earn_apr(venue, ccy, ts DESC);

CREATE TABLE IF NOT EXISTS fin_cex_spread (
    id          BIGSERIAL PRIMARY KEY,
    symbol      VARCHAR(32) NOT NULL,
    venue_buy   VARCHAR(16) NOT NULL,
    venue_sell  VARCHAR(16) NOT NULL,
    price_buy   NUMERIC(18,8) NOT NULL,
    price_sell  NUMERIC(18,8) NOT NULL,
    spread_bps  NUMERIC(10,4) NOT NULL,
    ts          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_cex_spread_symbol_ts ON fin_cex_spread(symbol, ts DESC);

INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, passphrase, base_url, simulated, website)
VALUES
    ('okx', 'spot', 'OKX 主网只读', '', '', '', '', false, 'https://www.okx.com'),
    ('okx', 'perp', 'OKX 主网只读', '', '', '', '', false, 'https://www.okx.com'),
    ('binance', 'spot', 'Binance 主网只读', '', '', '', 'https://192.229.85.35:8443/api', false, 'https://www.binance.com'),
    ('binance', 'perp', 'Binance 主网只读', '', '', '', 'https://192.229.85.35:8443/fapi', false, 'https://www.binance.com')
ON CONFLICT DO NOTHING;
