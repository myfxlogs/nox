-- 0093_system_settings.sql - 系统配置单行表（D-296）
-- tronscan_base: TronScan API 基址（空 = 回落 TRONSCAN_BASE env → DefaultTronscanBase）
-- 单行约束：id=1 CHECK，UPSERT 语义（同 ai_settings 模式）。
CREATE TABLE IF NOT EXISTS system_settings (
    id              INTEGER PRIMARY KEY DEFAULT 1,
    tronscan_base   TEXT,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT system_settings_single_row CHECK (id = 1)
);
INSERT INTO system_settings (id, tronscan_base)
VALUES (1, NULL)
ON CONFLICT (id) DO NOTHING;
