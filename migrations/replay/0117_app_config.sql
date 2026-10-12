-- 0117_app_config.sql - D-368 S1：运维旋钮 KV 收编（Bucket B 首批八键；秘密/安全开关留 env 不动）。
-- 零种子：不预置任何行——env 继续当 fallback 直到运维显式 INSERT（无静默行为漂移，D-368 S4）。
CREATE TABLE IF NOT EXISTS app_config (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    note       TEXT
);
