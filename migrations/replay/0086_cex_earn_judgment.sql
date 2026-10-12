-- D-273 Earn APR 判断引擎：判断结果表（upsert 去重，不无限增长）。
-- 每 venue+ccy+product_type 一行（最新判断），前端 CEX 扫描页"判断"列消费。
CREATE TABLE IF NOT EXISTS fin_cex_earn_judgment (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    venue        VARCHAR(16) NOT NULL,
    ccy          VARCHAR(16) NOT NULL,
    product_type VARCHAR(32) NOT NULL DEFAULT '',
    apr          NUMERIC(10,6) NOT NULL,
    category     VARCHAR(16) NOT NULL,  -- stable / token
    verdict      VARCHAR(20) NOT NULL,  -- opportunity / not_opportunity / trap
    reasons      TEXT NOT NULL,         -- JSON array of reason strings
    ts           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_cex_earn_judgment_venue_ccy_product
    ON fin_cex_earn_judgment (venue, ccy, product_type);
CREATE INDEX IF NOT EXISTS idx_cex_earn_judgment_ts ON fin_cex_earn_judgment (ts DESC);
