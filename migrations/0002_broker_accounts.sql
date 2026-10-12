-- 0002_broker_accounts.sql - Broker 管理器（docs/broker管理器实现方案.md §1）
-- 凭据明文存储：个人单机部署，公网入口仅经 auth token 保护。

CREATE TABLE broker_accounts (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    venue      TEXT NOT NULL UNIQUE,      -- 适配器标识（订阅 Id / Quote.Venue / 执行器路由键）
    platform   INT  NOT NULL,             -- 0=MT4, 1=MT5
    host       TEXT NOT NULL,
    port       INT  NOT NULL,
    user_id    BIGINT NOT NULL,
    password   TEXT NOT NULL,
    server     TEXT NOT NULL DEFAULT '',
    company    TEXT NOT NULL DEFAULT '',
    alias      TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'connected',  -- connected / failed / stopped
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE broker_symbol_map (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    venue          TEXT NOT NULL,
    broker_symbol  TEXT NOT NULL,
    canonical      TEXT NOT NULL,
    UNIQUE (venue, broker_symbol)
);
