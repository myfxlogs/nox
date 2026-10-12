-- 0010_exchange_configs.sql - CEX 交易所配置（docs/CEX交易所后台管理.md D-054）
-- 凭据明文存储：个人单机部署，公网入口仅经 auth token 保护（同 broker_accounts）。

CREATE TABLE exchange_configs (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    venue      TEXT NOT NULL,               -- binance / okx / 未来其他
    venue_type TEXT NOT NULL DEFAULT 'spot',
    label      TEXT NOT NULL DEFAULT '',    -- 别名
    api_key    TEXT NOT NULL,
    api_secret TEXT NOT NULL,
    passphrase TEXT NOT NULL DEFAULT '',    -- OKX
    base_url   TEXT NOT NULL DEFAULT '',    -- 空 = venue 默认（binance=代理、okx=实盘）
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (venue, venue_type)
);
