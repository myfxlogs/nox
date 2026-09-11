-- D-282：价格提醒规则引擎
CREATE TABLE price_alerts (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    symbol       TEXT NOT NULL,              -- canonical symbol（TRXUSDT/BTCUSDT）
    op           TEXT NOT NULL CHECK (op IN ('>=', '<=')),  -- 比较运算符
    threshold    DOUBLE PRECISION NOT NULL,   -- 阈值
    level        TEXT NOT NULL CHECK (level IN ('info','warn','critical')),
    message      TEXT NOT NULL,               -- 告警文案（如"TRX 涨到 0.34，补仓机会"）
    enabled      BOOLEAN NOT NULL DEFAULT true,
    last_alert_at TIMESTAMPTZ,                -- 上次告警时间（节流用）
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_price_alerts_symbol ON price_alerts (symbol) WHERE enabled = true;
