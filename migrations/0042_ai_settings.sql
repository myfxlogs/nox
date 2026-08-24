-- 0042_ai_settings.sql - AI 分析员配置单行表（D-091 返修② R3）
-- 前端可配勿硬编码：base_url/model/api_key/enabled 存 DB，AskAnalyst 每问读库（改完即生效免重启）。
-- 单行约束：id=1 CHECK，UPSERT 语义。
-- api_key 仅存 DB 不打日志；Get 响应掩码返回尾 4 位。

CREATE TABLE IF NOT EXISTS ai_settings (
    id         INTEGER PRIMARY KEY DEFAULT 1,
    base_url   TEXT,
    model      TEXT,
    api_key    TEXT,
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ai_settings_single_row CHECK (id = 1)
);

-- 初始行（UPSERT id=1，字段全空 → AskAnalyst 回落 .env ARB_AI_*）。
INSERT INTO ai_settings (id, base_url, model, api_key, enabled)
VALUES (1, NULL, NULL, NULL, TRUE)
ON CONFLICT (id) DO NOTHING;
