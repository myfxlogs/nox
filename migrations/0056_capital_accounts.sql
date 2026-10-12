-- 0056_capital_accounts.sql - D-193-B：资本账户身份与 venue 映射
-- 设计 SSOT: docs/步骤3资本事实与经济性系统-详细设计.md §3.1
-- CapitalAccount = 本金/风险/绩效单元；venue_accounts = 凭据/连接端点；不合并。

CREATE TABLE IF NOT EXISTS capital_accounts (
    account_id    bigint PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
    account_key   text NOT NULL UNIQUE,          -- 项目内唯一账户键
    currency      text NOT NULL DEFAULT 'USD',   -- 账户基础币种
    principal_usd double precision NOT NULL DEFAULT 1000, -- 固定任务书本金
    status        text NOT NULL DEFAULT 'ACTIVE', -- ACTIVE/SUSPENDED/CLOSED
    environment   text NOT NULL DEFAULT 'demo',  -- demo/live/paper
    opened_at     timestamptz NOT NULL DEFAULT now(),
    closed_at     timestamptz,
    CHECK (status = ANY (ARRAY['ACTIVE','SUSPENDED','CLOSED'])),
    CHECK (environment = ANY (ARRAY['demo','live','paper'])),
    CHECK (principal_usd = 1000)  -- 固定，不由运行时修改
);

CREATE TABLE IF NOT EXISTS capital_account_venue_links (
    id                bigint PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
    account_id        bigint NOT NULL REFERENCES capital_accounts(account_id),
    venue_account_key text NOT NULL,             -- venue:platform 格式（如 <venue>:mt5）
    environment       text NOT NULL,             -- 必须与 capital_accounts.environment 一致
    enabled           boolean NOT NULL DEFAULT true,
    created_at        timestamptz NOT NULL DEFAULT now(),
    disabled_at       timestamptz,
    UNIQUE (account_id, venue_account_key),
    CHECK (environment = ANY (ARRAY['demo','live','paper']))
);

CREATE INDEX IF NOT EXISTS capital_account_venue_links_venue_idx
    ON capital_account_venue_links (venue_account_key) WHERE enabled = true;
