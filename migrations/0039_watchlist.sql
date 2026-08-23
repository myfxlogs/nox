-- 0039_watchlist.sql - 总览自选报价条清单（D-090）
-- 业主自选 canonical 清单，service 层按此过滤 StreamUpdates case 1 推送。
-- 全量替换语义（SetWatchlist 事务内 DELETE + INSERT），sort=面板顺序。

CREATE TABLE IF NOT EXISTS watchlist_symbols (
    canonical text PRIMARY KEY,
    sort      integer NOT NULL DEFAULT 0,
    added_at  timestamptz NOT NULL DEFAULT now()
);
