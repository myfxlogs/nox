-- 0082_cnfi_repo_config.sql: D-270 R2——逆回购提醒配置 DB 化（前端可改免重启）。
-- 单行表 id=1 CHECK，UPSERT 语义（同 ai_settings 模式 D-091 R3）。
-- env 仍作 fallback：DB 为空/默认值时回落 env（首次部署零配置可用）。

CREATE TABLE IF NOT EXISTS cnfi_repo_config (
    id              INTEGER PRIMARY KEY DEFAULT 1,
    idle_cny        DOUBLE PRECISION NOT NULL DEFAULT 0,
    min_return_cny  DOUBLE PRECISION NOT NULL DEFAULT 0,
    reminder_hour   INTEGER NOT NULL DEFAULT 10,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT cnfi_repo_config_single_row CHECK (id = 1)
);

INSERT INTO cnfi_repo_config (id, idle_cny, min_return_cny, reminder_hour, enabled)
VALUES (1, 0, 0, 10, TRUE)
ON CONFLICT (id) DO NOTHING;
