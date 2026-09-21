-- 0118_radar_watchlist.sql - D-369 S2：确认信号 watchlist（radar 写/core 读，单一源）。
-- 仅写不删（last_seen 刷新；过期清理 v1 不做，SSOT §4）。
CREATE TABLE IF NOT EXISTS radar_watchlist (
    symbol     TEXT PRIMARY KEY,
    venues     TEXT[] NOT NULL,
    reason     TEXT NOT NULL,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now()
);
