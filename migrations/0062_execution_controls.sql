-- D-195-A §3.1.1：执行控制面持久化。
-- execution_controls = 当前状态（主键 scope_type+scope_key）；
-- execution_control_events = append-only 历史。

CREATE TABLE IF NOT EXISTS execution_controls (
    control_id    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    scope_type    TEXT NOT NULL CHECK (scope_type IN ('global','account')),
    scope_key     TEXT NOT NULL DEFAULT '',  -- global='' / account=account_key
    mode          TEXT NOT NULL CHECK (mode IN ('READ_ONLY','PAPER','TEST')),
    enabled       BOOLEAN NOT NULL DEFAULT true,
    approved_by   TEXT NOT NULL DEFAULT '',
    confirmed_at  TIMESTAMPTZ,
    expires_at    TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ,
    reason        TEXT NOT NULL DEFAULT '',
    config_digest TEXT NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (scope_type, scope_key)
);

CREATE TABLE IF NOT EXISTS execution_control_events (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    scope_type   TEXT NOT NULL CHECK (scope_type IN ('global','account')),
    scope_key    TEXT NOT NULL DEFAULT '',
    from_mode    TEXT NOT NULL DEFAULT '',
    to_mode      TEXT NOT NULL,
    actor        TEXT NOT NULL DEFAULT '',
    reason       TEXT NOT NULL DEFAULT '',
    evidence_ref TEXT NOT NULL DEFAULT '',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    event_key    TEXT NOT NULL  -- 幂等键
);

CREATE UNIQUE INDEX IF NOT EXISTS execution_control_events_key_idx
    ON execution_control_events (event_key);

-- 初始化 global READ_ONLY（步骤 5 默认）
INSERT INTO execution_controls (scope_type, scope_key, mode, enabled, confirmed_at, reason)
VALUES ('global', '', 'READ_ONLY', true, now(), 'D-195-A init')
ON CONFLICT (scope_type, scope_key) DO NOTHING;
