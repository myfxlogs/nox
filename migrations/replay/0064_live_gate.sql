-- 0064_live_gate.sql - D-195-C1 §3.2：LiveGate 持久记录
-- LiveGate 是导出 LIVE 执行模式的授权记录，不是执行模式本身。
-- LIVE 写入仍由步骤 6 PilotApproval 控制；本表只记录 LiveGate 8 条检查通过的事实。

CREATE TABLE IF NOT EXISTS live_gates (
    gate_id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    scope_type      TEXT NOT NULL CHECK (scope_type IN ('global','account')),
    scope_key       TEXT NOT NULL DEFAULT '',
    account_key     TEXT NOT NULL DEFAULT '',
    strategy        TEXT NOT NULL DEFAULT '',
    operator        TEXT NOT NULL,
    confirmed_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ,
    revoke_reason   TEXT NOT NULL DEFAULT '',
    config_digest   TEXT NOT NULL DEFAULT '',
    evidence_ref    TEXT NOT NULL DEFAULT '',
    -- 8 条检查通过的证据快照
    check_snapshot  JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (scope_type, scope_key, account_key, strategy)
);

CREATE INDEX IF NOT EXISTS live_gates_active_idx
    ON live_gates (scope_type, scope_key)
    WHERE revoked_at IS NULL;
