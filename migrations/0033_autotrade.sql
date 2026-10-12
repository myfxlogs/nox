-- D-070：自动交易引擎——交易日志 + 策略绩效跟踪
CREATE TABLE IF NOT EXISTS autotrade_logs (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ts             TIMESTAMPTZ NOT NULL DEFAULT now(),
    opportunity_id TEXT NOT NULL,
    position_id    BIGINT,
    strategy       TEXT NOT NULL,
    canonical      TEXT NOT NULL,
    venue_buy      TEXT NOT NULL,
    venue_sell     TEXT NOT NULL,
    qty            DOUBLE PRECISION NOT NULL,
    status         TEXT NOT NULL,
    reason         TEXT NOT NULL DEFAULT '',
    net_bps        DOUBLE PRECISION NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS autotrade_logs_ts_idx ON autotrade_logs (ts DESC);
CREATE INDEX IF NOT EXISTS autotrade_logs_strategy_idx ON autotrade_logs (strategy, ts DESC);

CREATE TABLE IF NOT EXISTS strategy_stats (
    strategy        TEXT PRIMARY KEY,
    total_trades    INT NOT NULL DEFAULT 0,
    wins            INT NOT NULL DEFAULT 0,
    losses          INT NOT NULL DEFAULT 0,
    total_pnl       DOUBLE PRECISION NOT NULL DEFAULT 0,
    avg_pnl         DOUBLE PRECISION NOT NULL DEFAULT 0,
    win_rate        DOUBLE PRECISION NOT NULL DEFAULT 0,
    disabled        BOOLEAN NOT NULL DEFAULT false,
    disabled_reason TEXT NOT NULL DEFAULT '',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
